package dashboard

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
	"claudgravity/internal/requestlog"
)

type stubTransport func(*http.Request) (*http.Response, error)

func (f stubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// adminEnv serves the dashboard routes with two real account files behind
// them. Google is stubbed: any outbound call answers with a bare token.
type adminEnv struct {
	srv  *httptest.Server
	pass string
	cm   *config.ConfigManager
	dir  string
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	dir := t.TempDir()
	for _, e := range []string{"a@example.com", "b@example.com"} {
		body := fmt.Sprintf(`{"email":%q,"access_token":"tok","refresh_token":"ref"}`, e)
		if err := os.WriteFile(filepath.Join(dir, e+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	restore := accounts.SetHTTPClientForTest(&http.Client{Transport: stubTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader(`{"access_token":"fresh","expires_in":3600}`))}, nil
	})})
	t.Cleanup(restore)
	t.Cleanup(accounts.ResetForTest)
	if err := accounts.InitAccountManager(dir, config.ServerConfig{}); err != nil {
		t.Fatal(err)
	}
	cm := testConfigManager(t)
	t.Cleanup(func() { accounts.SetBroadcaster(nil); requestlog.SetBroadcaster(nil) })
	mux := http.NewServeMux()
	RegisterDashboardRoutes(mux, cm)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &adminEnv{srv: srv, pass: cm.DashboardPassword(), cm: cm, dir: dir}
}

func (e *adminEnv) do(t *testing.T, method, path, body string, auth bool) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if auth {
		req.SetBasicAuth("admin", e.pass)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestAdminAPI_EveryEndpointRequiresTheDashboardPassword(t *testing.T) {
	e := newAdminEnv(t)
	for _, p := range []string{"/api/accounts", "/api/accounts/toggle?email=a@example.com&disabled=1", "/api/refresh-quotas",
		"/api/warmup?email=a@example.com", "/api/warmup-all", "/api/logs", "/api/sessions", "/api/session?id=x", "/api/model-catalog", "/api/keys"} {
		if code, _ := e.do(t, http.MethodPost, p, "", false); code != http.StatusUnauthorized {
			t.Errorf("%s without credentials = %d, want 401", p, code)
		}
	}
}

func TestAdminAPI_AccountsListDeleteAndToggle(t *testing.T) {
	e := newAdminEnv(t)

	code, body := e.do(t, http.MethodGet, "/api/accounts", "", true)
	var list []map[string]interface{}
	if code != 200 || json.Unmarshal([]byte(body), &list) != nil || len(list) != 2 {
		t.Fatalf("list: %d %s", code, body)
	}
	for _, a := range list {
		for k := range a {
			if strings.Contains(strings.ToLower(k), "token") {
				t.Fatalf("account listing must not expose %q", k)
			}
		}
	}

	if code, _ := e.do(t, http.MethodDelete, "/api/accounts", "", true); code != 400 {
		t.Fatalf("delete without email = %d", code)
	}
	if code, _ := e.do(t, http.MethodDelete, "/api/accounts?email=ghost@example.com", "", true); code != 404 {
		t.Fatalf("delete unknown = %d", code)
	}

	if code, _ := e.do(t, http.MethodGet, "/api/accounts/toggle?email=a@example.com&disabled=1", "", true); code != 405 {
		t.Fatalf("toggle by GET = %d, want 405", code)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/accounts/toggle?email=a@example.com", "", true); code != 400 {
		t.Fatalf("toggle without disabled = %d", code)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/accounts/toggle?email=ghost@example.com&disabled=true", "", true); code != 404 {
		t.Fatalf("toggle unknown = %d", code)
	}
	code, body = e.do(t, http.MethodPost, "/api/accounts/toggle?email=a@example.com&disabled=true", "", true)
	if code != 200 || !strings.Contains(body, `"disabled":true`) {
		t.Fatalf("toggle off: %d %s", code, body)
	}
	if acc, _ := accounts.Manager().GetAccount("a@example.com"); !acc.Disabled {
		t.Fatal("account was not disabled")
	}
	if code, body = e.do(t, http.MethodPost, "/api/accounts/toggle?email=a@example.com&disabled=0", "", true); code != 200 || !strings.Contains(body, `"disabled":false`) {
		t.Fatalf("toggle on: %d %s", code, body)
	}

	if code, body = e.do(t, http.MethodDelete, "/api/accounts?email=b@example.com", "", true); code != 200 || !strings.Contains(body, `"success":true`) {
		t.Fatalf("delete: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "b@example.com.json")); !os.IsNotExist(err) {
		t.Fatal("the account file must be removed from disk")
	}
	if _, ok := accounts.Manager().GetAccount("b@example.com"); ok {
		t.Fatal("deleted account still in the pool")
	}
}

func TestAdminAPI_RefreshAndWarmup(t *testing.T) {
	e := newAdminEnv(t)
	if code, body := e.do(t, http.MethodPost, "/api/refresh-quotas", "", true); code != 200 || !strings.Contains(body, "refresh_started") {
		t.Fatalf("refresh: %d %s", code, body)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/warmup", "", true); code != 400 {
		t.Fatalf("warmup without email = %d", code)
	}
	code, body := e.do(t, http.MethodPost, "/api/warmup?email=ghost@example.com", "", true)
	if code != http.StatusBadGateway || !strings.Contains(body, `"success":false`) {
		t.Fatalf("warmup unknown: %d %s", code, body)
	}
	// Neither account has a never-used weekly window, so nothing is warmed.
	code, body = e.do(t, http.MethodPost, "/api/warmup-all", "", true)
	var res struct {
		Success bool `json:"success"`
		Count   int  `json:"count"`
	}
	if code != 200 || json.Unmarshal([]byte(body), &res) != nil || !res.Success || res.Count != 0 {
		t.Fatalf("warmup-all: %d %s", code, body)
	}
	// The handler fires RefreshAllQuotas in the background; let it finish
	// before cleanup resets the package-level state it reads.
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		done := true
		for _, a := range accounts.Manager().GetAccounts() {
			done = done && !a.LastChecked.IsZero()
		}
		if done {
			break
		}
	}
	time.Sleep(100 * time.Millisecond)
}

func TestAdminAPI_LogsAndSessions(t *testing.T) {
	e := newAdminEnv(t)
	if err := requestlog.InitLogStore(filepath.Join(t.TempDir(), "logs.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(requestlog.Close)
	now := time.Now()
	requestlog.Add(requestlog.RequestLog{Timestamp: now, Key: "k1", Account: "a@example.com", SessionID: "sess-1", AgentKey: "main", Status: 200})
	requestlog.Add(requestlog.RequestLog{Timestamp: now, Key: "k1", Account: "a@example.com", SessionID: "sess-1", AgentKey: "main", Status: 500, Error: "boom"})
	requestlog.Add(requestlog.RequestLog{Timestamp: now, Key: "k2", Account: "b@example.com", SessionID: "sess-2", Status: 200})

	type logsResp struct {
		Logs  []requestlog.RequestLog `json:"logs"`
		Stats struct {
			TotalAccounts int `json:"total_accounts"`
			ActiveKeys    int `json:"active_keys"`
		} `json:"stats"`
	}
	get := func(q string) logsResp {
		code, body := e.do(t, http.MethodGet, "/api/logs"+q, "", true)
		var r logsResp
		if code != 200 || json.Unmarshal([]byte(body), &r) != nil {
			t.Fatalf("logs%s: %d %s", q, code, body)
		}
		return r
	}
	if r := get(""); len(r.Logs) != 3 || r.Stats.TotalAccounts != 2 {
		t.Fatalf("unfiltered: %d logs, %d accounts", len(r.Logs), r.Stats.TotalAccounts)
	}
	if r := get("?errors=true"); len(r.Logs) != 1 || r.Logs[0].Status != 500 {
		t.Fatalf("errors only: %+v", r.Logs)
	}
	if r := get("?session=sess-2"); len(r.Logs) != 1 {
		t.Fatalf("session filter: %d", len(r.Logs))
	}
	if r := get("?account=a@example.com"); len(r.Logs) != 2 {
		t.Fatalf("account filter: %d", len(r.Logs))
	}
	if r := get("?limit=1"); len(r.Logs) != 1 {
		t.Fatalf("limit: %d", len(r.Logs))
	}
	if r := get("?limit=garbage"); len(r.Logs) != 3 {
		t.Fatalf("a bad limit must fall back to the default: %d", len(r.Logs))
	}

	code, body := e.do(t, http.MethodGet, "/api/sessions?limit=1", "", true)
	var sessions []map[string]interface{}
	if code != 200 || json.Unmarshal([]byte(body), &sessions) != nil || len(sessions) != 1 {
		t.Fatalf("sessions: %d %s", code, body)
	}
	if code, _ = e.do(t, http.MethodGet, "/api/sessions", "", true); code != 200 {
		t.Fatalf("sessions default: %d", code)
	}

	if code, _ = e.do(t, http.MethodDelete, "/api/logs", "", true); code != 200 {
		t.Fatalf("clear: %d", code)
	}
	if r := get(""); len(r.Logs) != 0 {
		t.Fatalf("logs survived the clear: %d", len(r.Logs))
	}
}

func TestAdminAPI_KeyLifecycle(t *testing.T) {
	e := newAdminEnv(t)
	if code, _ := e.do(t, http.MethodPost, "/api/keys", "{not json", true); code != 400 {
		t.Fatalf("bad body = %d", code)
	}
	code, body := e.do(t, http.MethodPost, "/api/keys", `{"name":"ci","model":"gemini-3.8-flash"}`, true)
	var created struct{ Key, Name string }
	if code != 200 || json.Unmarshal([]byte(body), &created) != nil || created.Key == "" || created.Name != "ci" {
		t.Fatalf("create: %d %s", code, body)
	}

	code, body = e.do(t, http.MethodGet, "/api/keys", "", true)
	if code != 200 || strings.Contains(body, created.Key) || !strings.Contains(body, requestlog.MaskKey(created.Key)) {
		t.Fatalf("listing must show only the masked key: %d %s", code, body)
	}

	if code, _ = e.do(t, http.MethodDelete, "/api/keys?key="+created.Key, "", true); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if _, ok := e.cm.GetAllTenants()[created.Key]; ok {
		t.Fatal("key still present after deletion")
	}

	// Deleting by the masked form works when it is unambiguous.
	_, body = e.do(t, http.MethodPost, "/api/keys", `{"name":"by-mask"}`, true)
	var second struct{ Key string }
	_ = json.Unmarshal([]byte(body), &second)
	e.do(t, http.MethodDelete, "/api/keys?key="+requestlog.MaskKey(second.Key), "", true)
	if _, ok := e.cm.GetAllTenants()[second.Key]; ok {
		t.Fatal("key was not deleted through its masked form")
	}
}
