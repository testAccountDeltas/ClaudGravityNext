package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
	"claudgravity/internal/testutil"
)

const searchReq = `{"model":"claude-3-5-haiku-20241022","max_tokens":64,"tools":[{"name":"web_search","type":"web_search_20250305"}],` +
	`"messages":[{"role":"user","content":"Perform a web search for the query: golang"}]}`

type searchEnv struct {
	srv *httptest.Server
	mu  sync.Mutex
	req []*http.Request
}

func (s *searchEnv) google() (n int, auth []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.req {
		if strings.Contains(r.URL.Host, "googleapis.com") {
			n++
			auth = append(auth, r.Header.Get("Authorization"))
		}
	}
	return
}

func newSearchEnv(t *testing.T, tenant map[string]interface{}) *searchEnv {
	t.Helper()
	testutil.SeedAccounts(t, 1)
	cfg, _ := json.Marshal(map[string]interface{}{"tenants": map[string]interface{}{"sk-ws": tenant}})
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	cm, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	env := &searchEnv{}
	orig := websearchHTTPClient
	websearchHTTPClient = &http.Client{Transport: testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		env.mu.Lock()
		env.req = append(env.req, r)
		env.mu.Unlock()
		if strings.Contains(r.URL.Host, "googleapis.com") {
			return testutil.Response(200, `{"response":{"candidates":[{"content":{"parts":[{"text":"Go."}]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://go.dev","title":"Go"}}]}}]}}`), nil
		}
		return testutil.Response(200, `{"query":{"search":[{"title":"Go","snippet":"x"}]}}`), nil
	})}
	t.Cleanup(func() { websearchHTTPClient = orig })
	env.srv = httptest.NewServer(NewRouter(cm))
	t.Cleanup(env.srv.Close)
	return env
}

func (s *searchEnv) search(t *testing.T) string {
	t.Helper()
	req, _ := http.NewRequest("POST", s.srv.URL+"/v1/messages", strings.NewReader(searchReq))
	req.Header.Set("x-api-key", "sk-ws")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	return string(b)
}

func TestWebSearchIntercept_OAuthTenantUsesAccountToken(t *testing.T) {
	e := newSearchEnv(t, map[string]interface{}{"name": "t", "google_auth_type": "oauth"})
	body := e.search(t)
	if gjson.Get(body, "content.1.content.0.url").String() != "https://go.dev" {
		t.Fatalf("body %s", body)
	}
	n, auth := e.google()
	if n != 1 || !strings.HasPrefix(auth[0], "Bearer ") || strings.TrimSpace(strings.TrimPrefix(auth[0], "Bearer")) == "" {
		t.Fatalf("google calls %d auth %q", n, auth)
	}
}

func TestWebSearchIntercept_PlaceholderAPIKeyTreatedAsOAuth(t *testing.T) {
	e := newSearchEnv(t, map[string]interface{}{"name": "t", "google_auth_type": "api_key", "google_api_key": "YOUR_GEMINI_API_KEY_HERE"})
	e.search(t)
	if n, _ := e.google(); n != 1 {
		t.Fatalf("placeholder key should fall back to the account pool (google calls %d)", n)
	}
}

func TestWebSearchIntercept_RealAPIKeyTenantNeverCallsGroundingWithAccountToken(t *testing.T) {
	e := newSearchEnv(t, map[string]interface{}{"name": "t", "google_auth_type": "api_key", "google_api_key": "AIzaRealKey"})
	e.search(t)
	if n, _ := e.google(); n != 0 {
		t.Fatalf("api_key tenant triggered %d Google grounding calls", n)
	}
}

func TestWebSearchIntercept_RefreshFailureSendsNoEmptyBearer(t *testing.T) {
	e := newSearchEnv(t, map[string]interface{}{"name": "t", "google_auth_type": "oauth"})
	// Seeded accounts have no expiry, so every call refreshes: make it fail.
	restore := accounts.SetHTTPClientForTest(&http.Client{Transport: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return testutil.Response(500, `{"error":"down"}`), nil
	})})
	t.Cleanup(restore)
	body := e.search(t)
	if gjson.Get(body, "content.1.type").String() != "web_search_tool_result" {
		t.Fatalf("body %s", body)
	}
	_, auth := e.google()
	for _, a := range auth {
		if strings.TrimSpace(strings.TrimPrefix(a, "Bearer")) == "" {
			t.Fatalf("empty bearer sent to Google: %q", a)
		}
	}
}
