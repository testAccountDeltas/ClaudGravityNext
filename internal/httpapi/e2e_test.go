package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
	"claudgravity/internal/orchestrate"
	"claudgravity/internal/testutil"
)

// End-to-end tests: real router + handler + translate + pipeline + stream
// against a fake Gemini transport.

const e2eKey = "sk-e2e"

type e2eEnv struct {
	srv   *httptest.Server
	calls int32
}

func newE2E(t *testing.T, accountsN int, upstream func(n int, body string) (int, string)) *e2eEnv {
	t.Helper()
	testutil.SeedAccounts(t, accountsN)
	cfg, _ := json.Marshal(map[string]interface{}{"tenants": map[string]interface{}{e2eKey: map[string]interface{}{
		"name": "e2e", "google_auth_type": "oauth", "default_model": "gemini-3.8-flash-tiered"}}})
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	cm, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	env := &e2eEnv{}
	t.Cleanup(orchestrate.SetTransportForTest(testutil.RoundTripFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		n := int(atomic.AddInt32(&env.calls, 1))
		st, body := upstream(n, string(b))
		if st == 200 && strings.Contains(r.URL.Path, "stream") {
			body = testutil.SSE(body) // fakes return one envelope; wrap it for streaming
		}
		return testutil.Response(st, body), nil
	})))
	env.srv = httptest.NewServer(NewRouter(cm))
	t.Cleanup(env.srv.Close)
	return env
}

func (e *e2eEnv) post(t *testing.T, path, key, body string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func okTurn(text string) string {
	return testutil.GeminiChunk(testutil.TextCandidate(text, "STOP"), 120, 7)
}

const e2eReq = `{"model":"claude-sonnet-4-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]%s}`

func TestE2E_NonStream_HappyPath(t *testing.T) {
	e := newE2E(t, 1, func(int, string) (int, string) { return 200, okTurn("hello world") })
	st, body, _ := e.post(t, "/v1/messages", e2eKey, strings.Replace(e2eReq, "%s", "", 1))
	if st != 200 {
		t.Fatalf("status %d: %s", st, body)
	}
	if gjson.Get(body, "content.0.text").String() != "hello world" || gjson.Get(body, "stop_reason").String() != "end_turn" {
		t.Fatalf("unexpected body: %s", body)
	}
	if gjson.Get(body, "usage.input_tokens").Int() != 120 || gjson.Get(body, "usage.output_tokens").Int() != 7 {
		t.Fatalf("usage not propagated: %s", gjson.Get(body, "usage").Raw)
	}
}

func TestE2E_Stream_EventSequence(t *testing.T) {
	e := newE2E(t, 1, func(int, string) (int, string) { return 200, okTurn("streamed text") })
	st, body, h := e.post(t, "/v1/messages", e2eKey, strings.Replace(e2eReq, "%s", `,"stream":true`, 1))
	if st != 200 || !strings.HasPrefix(h.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status %d ct %q", st, h.Get("Content-Type"))
	}
	var events []string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "event: ") {
			events = append(events, strings.TrimPrefix(l, "event: "))
		}
	}
	want := []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v, want %v", events, want)
	}
	if !strings.Contains(body, "streamed text") || !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("body: %s", body)
	}
}

func TestE2E_FailoverOn429(t *testing.T) {
	e := newE2E(t, 2, func(n int, _ string) (int, string) {
		if n == 1 {
			return 429, `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota"}}`
		}
		return 200, okTurn("second account answered")
	})
	st, body, _ := e.post(t, "/v1/messages", e2eKey, strings.Replace(e2eReq, "%s", "", 1))
	if st != 200 || !strings.Contains(body, "second account answered") {
		t.Fatalf("status %d body %s", st, body)
	}
	if atomic.LoadInt32(&e.calls) != 2 {
		t.Fatalf("want 2 upstream calls, got %d", e.calls)
	}
}

func TestE2E_InvalidRequestsNeverReachUpstream(t *testing.T) {
	e := newE2E(t, 1, func(int, string) (int, string) { return 200, okTurn("x") })
	cases := map[string]struct {
		key, body string
		want      int
	}{
		"no key":         {"", strings.Replace(e2eReq, "%s", "", 1), 401},
		"wrong key":      {"sk-nope", strings.Replace(e2eReq, "%s", "", 1), 401},
		"max_tokens -1":  {e2eKey, `{"model":"claude-sonnet-4-5","max_tokens":-1,"messages":[{"role":"user","content":"hi"}]}`, 400},
		"empty messages": {e2eKey, `{"model":"claude-sonnet-4-5","max_tokens":5,"messages":[]}`, 400},
		"bad edits":      {e2eKey, strings.Replace(e2eReq, "%s", `,"context_management":{"edits":[{"type":"clear_tool_uses_20250919"},{"type":"clear_thinking_20251015"}]}`, 1), 400},
	}
	for name, c := range cases {
		st, body, _ := e.post(t, "/v1/messages", c.key, c.body)
		if st != c.want {
			t.Errorf("%s: status %d want %d (%s)", name, st, c.want, body)
		}
		if c.want >= 400 && gjson.Get(body, "type").String() != "error" {
			t.Errorf("%s: not an Anthropic error body: %s", name, body)
		}
	}
	if n := atomic.LoadInt32(&e.calls); n != 0 {
		t.Fatalf("invalid requests reached upstream %d times", n)
	}
}

func TestE2E_ContextEditingShrinksUpstreamBody(t *testing.T) {
	var sent string
	e := newE2E(t, 1, func(_ int, b string) (int, string) { sent = b; return 200, okTurn("ok") })
	body := `{"model":"claude-sonnet-4-5","max_tokens":20,"tools":[{"name":"Read","description":"r","input_schema":{"type":"object","properties":{"p":{"type":"string"}}}}],"messages":[{"role":"user","content":"go"}` +
		`,{"role":"assistant","content":[{"type":"tool_use","id":"a1","name":"Read","input":{"p":"x"}}]}` +
		`,{"role":"user","content":[{"type":"tool_result","tool_use_id":"a1","content":"` + strings.Repeat("BIGDATA ", 300) + `"}]}` +
		`,{"role":"assistant","content":[{"type":"tool_use","id":"a2","name":"Read","input":{"p":"y"}}]}` +
		`,{"role":"user","content":[{"type":"tool_result","tool_use_id":"a2","content":"small"}]}` +
		`],"context_management":{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":1},"keep":{"type":"tool_uses","value":1}}]}}`
	st, resp, _ := e.post(t, "/v1/messages", e2eKey, body)
	if st != 200 {
		t.Fatalf("status %d: %s", st, resp)
	}
	if strings.Contains(sent, "BIGDATA") || !strings.Contains(sent, "small") {
		t.Fatalf("old tool result must be cleared and the new one kept; upstream body: %.400s", sent)
	}
	if gjson.Get(resp, "context_management.applied_edits.0.cleared_tool_uses").Int() != 1 {
		t.Fatalf("applied_edits missing: %s", resp)
	}
}
