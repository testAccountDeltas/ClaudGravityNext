package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts"
	"claudgravity/internal/testutil"
)

func (e *e2eEnv) call(t *testing.T, method, path string, hdr map[string]string, body string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, sb.String(), resp.Header
}

var keyHdr = map[string]string{"x-api-key": e2eKey}

func noUpstream(t *testing.T) func(int, string) (int, string) {
	return func(int, string) (int, string) {
		t.Error("upstream must not be called by this endpoint")
		return 500, ""
	}
}

func TestCountTokens_Contract(t *testing.T) {
	e := newE2E(t, 1, noUpstream(t))
	const ok = `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello there"}]}`

	st, _, h := e.call(t, "GET", "/v1/messages/count_tokens", keyHdr, "")
	if st != 405 || h.Get("Allow") != "POST" {
		t.Fatalf("GET: %d allow=%q", st, h.Get("Allow"))
	}
	if st, _, _ = e.call(t, "POST", "/v1/messages/count_tokens", nil, ok); st != 401 {
		t.Fatalf("no key: %d", st)
	}
	for name, body := range map[string]string{
		"not json":       `{oops`,
		"no model":       `{"messages":[{"role":"user","content":"x"}]}`,
		"no messages":    `{"model":"m"}`,
		"empty messages": `{"model":"m","messages":[]}`,
		"string msgs":    `{"model":"m","messages":"x"}`,
	} {
		if st, b, _ := e.call(t, "POST", "/v1/messages/count_tokens", keyHdr, body); st != 400 || !strings.Contains(b, "invalid_request_error") {
			t.Errorf("%s: %d %s", name, st, b)
		}
	}

	st, b, h := e.call(t, "POST", "/count_tokens", keyHdr, ok)
	if st != 200 || gjson.Get(b, "input_tokens").Int() <= 0 {
		t.Fatalf("count: %d %s", st, b)
	}
	if h.Get("anthropic-ratelimit-unified-status") == "" && h.Get("anthropic-organization-id") == "" {
		t.Fatalf("rate-limit/identity headers missing: %v", h)
	}
	if gjson.Get(b, "context_management").Exists() {
		t.Fatalf("context_management must only appear when requested: %s", b)
	}
}

func TestCountTokens_ContextManagementAndBetaHeader(t *testing.T) {
	e := newE2E(t, 1, noUpstream(t))
	long := strings.Repeat("some tool output line\n", 400)
	body := `{"model":"claude-sonnet-4-5","messages":[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"` + strings.ReplaceAll(long, "\n", `\n`) + `"}]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"Read","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"short"}]}],` +
		`"context_management":{"edits":[{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":1},"keep":{"type":"tool_uses","value":1}}]}}`
	hdr := map[string]string{"x-api-key": e2eKey, "anthropic-beta": "context-management-2025-06-27"}
	st, b, _ := e.call(t, "POST", "/v1/messages/count_tokens", hdr, body)
	if st != 200 {
		t.Fatalf("%d %s", st, b)
	}
	orig, now := gjson.Get(b, "context_management.original_input_tokens").Int(), gjson.Get(b, "input_tokens").Int()
	if orig <= now || now <= 0 {
		t.Fatalf("editing must shrink the count: original=%d after=%d", orig, now)
	}

	// Unknown strategies are skipped on purpose (a newer client may send
	// them), but a malformed container is a client error.
	unknown := strings.Replace(body, `"clear_tool_uses_20250919"`, `"clear_everything"`, 1)
	if st, b, _ = e.call(t, "POST", "/v1/messages/count_tokens", hdr, unknown); st != 200 || gjson.Get(b, "context_management.original_input_tokens").Int() != gjson.Get(b, "input_tokens").Int() {
		t.Fatalf("an unknown strategy must be ignored, not applied or rejected: %d %s", st, b)
	}
	bad := strings.Replace(body, `"edits":[`, `"edits":{"x":[`, 1)
	bad = strings.Replace(bad, `}}]}}`, `}}]}}}`, 1)
	if st, b, _ = e.call(t, "POST", "/v1/messages/count_tokens", hdr, bad); st != 400 {
		t.Fatalf("malformed edits must be rejected: %d %s", st, b)
	}
}

func TestLegacyAndUnknownV1Paths(t *testing.T) {
	e := newE2E(t, 1, noUpstream(t))
	if st, _, h := e.call(t, "GET", "/v1/messages/cache_touch", keyHdr, ""); st != 405 || h.Get("Allow") != "POST" {
		t.Fatalf("cache_touch GET: %d", st)
	}
	if st, b, _ := e.call(t, "POST", "/v1/messages/cache_touch", keyHdr, ""); st != 200 || !strings.Contains(b, "touched") {
		t.Fatalf("cache_touch POST: %d %s", st, b)
	}
	if st, b, _ := e.call(t, "POST", "/v1/complete", keyHdr, "{}"); st != 400 || !strings.Contains(b, "deprecated") {
		t.Fatalf("complete: %d %s", st, b)
	}
	if st, b, _ := e.call(t, "POST", "/v1/messages/batches", keyHdr, "{}"); st != 400 || !strings.Contains(b, "batches") {
		t.Fatalf("batches: %d %s", st, b)
	}
	if st, b, _ := e.call(t, "GET", "/v1/messages/anything-else", keyHdr, ""); st != 404 || !strings.Contains(b, "not_found_error") {
		t.Fatalf("unknown sub-path: %d %s", st, b)
	}
	if st, b, _ := e.call(t, "GET", "/v1/nonsense", keyHdr, ""); st != 404 || !strings.Contains(b, "/v1/nonsense") {
		t.Fatalf("unknown v1 path: %d %s", st, b)
	}
	if st, _, h := e.call(t, "GET", "/v1/messages/", keyHdr, ""); st != 405 || h.Get("Allow") != "POST" {
		t.Fatalf("messages/ GET: %d", st)
	}
}

func TestModels_ListPagingAndLookup(t *testing.T) {
	restore := accounts.SetCatalogForTest([]accounts.AvailableModelInfo{
		{ID: "gemini-live-only", DisplayName: "Live Only", MaxTokens: 123456, MaxOutput: 4096, Thinking: true},
		{ID: "claude-sonnet-4-5", DisplayName: "dup of a static entry"},
		{ID: ""},
	})
	t.Cleanup(restore)
	e := newE2E(t, 1, noUpstream(t))

	if st, _, h := e.call(t, "POST", "/v1/models", keyHdr, ""); st != 405 || h.Get("Allow") != "GET" {
		t.Fatalf("POST /v1/models: %d", st)
	}
	if st, _, _ := e.call(t, "GET", "/v1/models", nil, ""); st != 401 {
		t.Fatalf("no key: %d", st)
	}
	for _, q := range []string{"?limit=0", "?limit=abc", "?after_id=nope", "?before_id=nope"} {
		if st, b, _ := e.call(t, "GET", "/v1/models"+q, keyHdr, ""); st != 400 || !strings.Contains(b, "invalid_request_error") {
			t.Errorf("%s: %d %s", q, st, b)
		}
	}

	st, b, _ := e.call(t, "GET", "/v1/models?limit=1000", keyHdr, "")
	if st != 200 {
		t.Fatalf("list: %d %s", st, b)
	}
	var ids []string
	for _, m := range gjson.Get(b, "data").Array() {
		ids = append(ids, m.Get("id").String())
	}
	if !contains(ids, "gemini-live-only") || count(ids, "claude-sonnet-4-5") != 1 {
		t.Fatalf("catalog merge wrong (live model missing or static entry duplicated): %v", ids)
	}
	first := gjson.Get(b, "first_id").String()
	if first != ids[0] || gjson.Get(b, "last_id").String() != ids[len(ids)-1] {
		t.Fatalf("cursor ids do not match the page: %s", b)
	}

	st, b, _ = e.call(t, "GET", "/v1/models?limit=2", keyHdr, "")
	if st != 200 || len(gjson.Get(b, "data").Array()) != 2 || !gjson.Get(b, "has_more").Bool() {
		t.Fatalf("page 1: %d %s", st, b)
	}
	last := gjson.Get(b, "last_id").String()
	st, b, _ = e.call(t, "GET", "/v1/models?limit=2&after_id="+last, keyHdr, "")
	if st != 200 || gjson.Get(b, "data.0.id").String() == first {
		t.Fatalf("page 2 must continue after the cursor: %d %s", st, b)
	}

	st, b, _ = e.call(t, "GET", "/v1/models/gemini-live-only", keyHdr, "")
	if st != 200 || gjson.Get(b, "id").String() != "gemini-live-only" {
		t.Fatalf("by id: %d %s", st, b)
	}
	if st, _, _ = e.call(t, "GET", "/v1/models/no-such-model", keyHdr, ""); st != 404 {
		t.Fatalf("unknown id: %d", st)
	}
	if st, _, _ = e.call(t, "DELETE", "/v1/models/gemini-live-only", keyHdr, ""); st != 405 {
		t.Fatalf("DELETE by id: %d", st)
	}
	if st, _, _ = e.call(t, "GET", "/v1/models/gemini-live-only", nil, ""); st != 401 {
		t.Fatalf("by id without key: %d", st)
	}
}

func contains(s []string, v string) bool { return count(s, v) > 0 }

func count(s []string, v string) (n int) {
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}

func TestUsage_AuthMethodAndAPIKeyTenant(t *testing.T) {
	cm := newTestConfigManager(t, "sk-direct")
	srv := httptest.NewServer(NewRouter(cm))
	t.Cleanup(srv.Close)
	get := func(method, key string) (int, string, http.Header) {
		req, _ := http.NewRequest(method, srv.URL+"/v1/usage", nil)
		if key != "" {
			req.Header.Set("x-api-key", key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var b bytes.Buffer
		_, _ = b.ReadFrom(resp.Body)
		return resp.StatusCode, b.String(), resp.Header
	}
	if st, _, h := get("POST", "sk-direct"); st != 405 || h.Get("Allow") != "GET" {
		t.Fatalf("POST: %d", st)
	}
	if st, _, _ := get("GET", ""); st != 401 {
		t.Fatalf("no key: %d", st)
	}
	st, b, _ := get("GET", "sk-direct")
	if st != 200 || !strings.Contains(b, "direct API key") || gjson.Get(b, "groups.0.buckets.#").Int() != 2 {
		t.Fatalf("direct-key tenants get a clearly synthetic answer: %d %s", st, b)
	}
}

func TestUsage_ReportsTheStickyOrConversationAccountQuota(t *testing.T) {
	e := newE2E(t, 2, noUpstream(t))
	const quota = `{"groups":[{"displayName":"Gemini Models","buckets":[` +
		`{"bucketId":"w","window":"weekly","remainingFraction":0.731},` +
		`{"bucketId":"u","window":"5h"}]}]}`
	restore := accounts.SetHTTPClientForTest(&http.Client{Transport: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return testutil.Response(200, quota), nil
	})})
	t.Cleanup(restore)

	// Conversation affinity wins: bind the conversation to account 1.
	conv := "conv-usage-test"
	accounts.GlobalSelector().RememberAffinity(conv, "tu-1@example.com")
	st, b, _ := e.call(t, "GET", "/v1/usage?conversation="+conv, keyHdr, "")
	if st != 200 || gjson.Get(b, "account").String() != "tu-1@example.com" {
		t.Fatalf("conversation account: %d %s", st, b)
	}
	// 73.1 is reported; the bucket without a fraction is hidden, not shown as 0%.
	buckets := gjson.Get(b, "groups.0.buckets").Array()
	if len(buckets) != 1 || buckets[0].Get("remaining_percent").Float() != 73.1 {
		t.Fatalf("buckets: %s", gjson.Get(b, "groups").Raw)
	}

	// The session header derives the same kind of anchor.
	anchor := accounts.DeriveConversationAnchor(nil, "sess-abc", "")
	accounts.GlobalSelector().RememberAffinity(anchor, "tu-0@example.com")
	hdr := map[string]string{"x-api-key": e2eKey, "x-claude-code-session-id": "sess-abc"}
	if _, b, _ = e.call(t, "GET", "/v1/usage", hdr, ""); gjson.Get(b, "account").String() != "tu-0@example.com" {
		t.Fatalf("session-header account: %s", b)
	}

	// Without either, the tenant gets a sticky account from the pool.
	if st, b, _ = e.call(t, "GET", "/v1/usage", keyHdr, ""); st != 200 || gjson.Get(b, "account").String() == "" {
		t.Fatalf("sticky fallback: %d %s", st, b)
	}
	// Authorization: Bearer works like x-api-key.
	if st, _, _ = e.call(t, "GET", "/v1/usage", map[string]string{"Authorization": "Bearer " + e2eKey}, ""); st != 200 {
		t.Fatalf("bearer: %d", st)
	}
	time.Sleep(50 * time.Millisecond) // the stale-quota refresh may still be running
}

func TestRemapExhaustedNative3PModel(t *testing.T) {
	testutil.SeedAccounts(t, 1)
	restore := accounts.SetHTTPClientForTest(&http.Client{Transport: testutil.RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return testutil.Response(200, `{"groups":[{"displayName":"Claude and GPT","buckets":[{"bucketId":"b","window":"5h","remainingFraction":0.0}]}]}`), nil
	})})
	t.Cleanup(restore)
	if err := accounts.Manager().FetchQuota("tu-0@example.com"); err != nil {
		t.Fatal(err)
	}
	const conv = "conv-remap"
	body := func(model string) []byte {
		b, _ := json.Marshal(map[string]interface{}{"model": model, "messages": []interface{}{}})
		return b
	}

	// No sticky account yet: nothing to peek at, the request is untouched.
	if got := remapExhaustedNative3PModel(1, conv, body("claude-sonnet-4-6")); gjson.GetBytes(got, "model").String() != "claude-sonnet-4-6" {
		t.Fatalf("remapped without an account: %s", got)
	}
	accounts.GlobalSelector().RememberAffinity(conv, "tu-0@example.com")

	for _, m := range []string{"claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b-medium"} {
		if got := remapExhaustedNative3PModel(1, conv, body(m)); gjson.GetBytes(got, "model").String() != "gemini-3.8-flash-tiered" {
			t.Errorf("%s must move to Gemini when the sticky account has no 3P quota: %s", m, got)
		}
	}
	for _, m := range []string{"claude-sonnet-4-5", "gemini-3.8-flash-tiered", ""} {
		if got := remapExhaustedNative3PModel(1, conv, body(m)); gjson.GetBytes(got, "model").String() != m {
			t.Errorf("%q is not a native 3P model and must be left alone: %s", m, got)
		}
	}
	// A sticky account that still has quota keeps its model.
	accounts.GlobalSelector().RememberAffinity("conv-ok", "ghost@example.com")
	if got := remapExhaustedNative3PModel(1, "conv-ok", body("claude-sonnet-4-6")); gjson.GetBytes(got, "model").String() != "claude-sonnet-4-6" {
		t.Fatalf("unknown account: %s", got)
	}
}

func TestRetryAfterFromRecords(t *testing.T) {
	now := time.Now()
	rec := func(disabled bool, cool time.Duration) *accounts.Record {
		return &accounts.Record{Disabled: disabled, CooldownUntil: now.Add(cool)}
	}
	for _, c := range []struct {
		name string
		recs []*accounts.Record
		want time.Duration
	}{
		{"empty pool", nil, defaultRetryAfter},
		{"a usable account", []*accounts.Record{rec(false, -time.Minute)}, idleRetryAfter},
		{"soonest cooldown wins", []*accounts.Record{rec(false, 90*time.Second), rec(false, 30*time.Second)}, 30 * time.Second},
		{"clamped up to a second", []*accounts.Record{rec(false, 10*time.Millisecond)}, time.Second},
		{"clamped down to the maximum", []*accounts.Record{rec(false, 10*time.Hour)}, maxRetryAfter},
		{"only disabled accounts", []*accounts.Record{rec(true, 0)}, defaultRetryAfter},
		{"nil entries are skipped", []*accounts.Record{nil, rec(false, 45*time.Second)}, 45 * time.Second},
	} {
		if got := retryAfterFromRecords(c.recs, now); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
