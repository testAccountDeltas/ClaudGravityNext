package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const quota429 = `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource has been exhausted (e.g. check quota)."}}`

// A pool-wide 429 must reach the client with Retry-After / x-should-retry,
// a short sanitised message and the request id (no raw Google JSON).
func TestE2E_PoolWide429_HeadersAndMessage(t *testing.T) {
	e := newE2E(t, 2, func(int, string) (int, string) { return 429, quota429 })
	st, body, h := e.post(t, "/v1/messages", e2eKey, strings.Replace(e2eReq, "%s", "", 1))
	if st != 429 {
		t.Fatalf("status %d: %s", st, body)
	}
	ra, err := strconv.Atoi(h.Get("Retry-After"))
	if err != nil || ra < 1 || ra > 900 {
		t.Fatalf("Retry-After %q", h.Get("Retry-After"))
	}
	if h.Get("x-should-retry") != "true" {
		t.Fatalf("x-should-retry %q", h.Get("x-should-retry"))
	}
	if gjson.Get(body, "error.type").String() != "rate_limit_error" {
		t.Fatalf("body %s", body)
	}
	msg := gjson.Get(body, "error.message").String()
	if strings.ContainsAny(msg, "{}") || strings.Contains(msg, "RESOURCE_EXHAUSTED") || len(msg) > 300 || msg == "" {
		t.Fatalf("message not sanitised: %q", msg)
	}
	if gjson.Get(body, "request_id").String() != h.Get("request-id") || h.Get("request-id") == "" {
		t.Fatalf("request id missing/mismatch: %s vs %q", body, h.Get("request-id"))
	}
}

// Upstream 503 on the only account puts it in cooldown; the client sees a
// retryable pool-exhausted error (today a 429), with Retry-After.
func TestE2E_PoolWide503_ShouldRetry(t *testing.T) {
	e := newE2E(t, 1, func(int, string) (int, string) {
		return 503, `{"error":{"code":503,"status":"UNAVAILABLE","message":"The model is overloaded."}}`
	})
	st, body, h := e.post(t, "/v1/messages", e2eKey, strings.Replace(e2eReq, "%s", "", 1))
	if st != 429 && st != 503 && st != 529 {
		t.Fatalf("status %d: %s", st, body)
	}
	if h.Get("x-should-retry") != "true" || h.Get("Retry-After") == "" {
		t.Fatalf("headers: should-retry=%q retry-after=%q", h.Get("x-should-retry"), h.Get("Retry-After"))
	}
	if msg := gjson.Get(body, "error.message").String(); msg == "" || strings.ContainsAny(msg, "{}") {
		t.Fatalf("message %q", msg)
	}
}

// Dated / aliased Claude model ids are accepted through the full path.
func TestE2E_ModelIDNormalization(t *testing.T) {
	e := newE2E(t, 1, func(int, string) (int, string) { return 200, okTurn("fine") })
	for _, m := range []string{
		"claude-sonnet-4-5", "claude-sonnet-4-5-20250929", "claude-3-5-haiku-20241022",
		"claude-3-7-sonnet-latest", "sonnet", "claude-opus-4-5-20251101",
	} {
		body := `{"model":"` + m + `","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`
		st, resp, _ := e.post(t, "/v1/messages", e2eKey, body)
		if st != 200 || gjson.Get(resp, "content.0.text").String() != "fine" {
			t.Errorf("model %q: status %d body %.200s", m, st, resp)
		}
	}
}

func TestCORSPreflight_AllowList(t *testing.T) {
	e := newE2E(t, 1, func(int, string) (int, string) { return 200, okTurn("x") })
	st, _, h := e.do(t, http.MethodOptions, "/v1/messages", nil, "")
	if st != http.StatusNoContent {
		t.Fatalf("status %d", st)
	}
	allow := strings.ToLower(h.Get("Access-Control-Allow-Headers"))
	for _, want := range []string{"anthropic-beta", "x-claude-code-request-class", "x-api-key", "authorization", "anthropic-version"} {
		if !strings.Contains(allow, want) {
			t.Errorf("Access-Control-Allow-Headers lacks %q", want)
		}
	}
	if !strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") {
		t.Errorf("methods %q", h.Get("Access-Control-Allow-Methods"))
	}
}

func TestV1UnknownPath_404InAnthropicFormat(t *testing.T) {
	e := newE2E(t, 1, func(int, string) (int, string) { return 200, okTurn("x") })
	st, body, _ := e.do(t, "GET", "/v1/nope", map[string]string{"x-api-key": e2eKey}, "")
	if st != 404 || gjson.Get(body, "error.type").String() != "not_found_error" {
		t.Fatalf("status %d body %s", st, body)
	}
}
