package orchestrate

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts"
)

// What a generation call looks like to Google. The Antigravity client sends a
// short, fixed set of headers and a request without safetySettings or (for a
// Gemini model) maxOutputTokens; a third-party client that adds identities of
// other products (the Cloud Shell editor's X-Goog-Api-Client, gemini-cli's
// Client-Metadata) or switches every safety category off is easy to tell apart.
// The account that was banned for a Terms of Service violation prompted this
// test; the shape follows the reference relay, which has not been banned.
func TestUpstreamIdentity_GenerationCallLooksLikeTheAntigravityClient(t *testing.T) {
	claude := `{"model":"claude-sonnet-4-5","max_tokens":1024,"system":"You are a helpful assistant.","messages":[{"role":"user","content":"hi"}]}`
	for _, streaming := range []bool{false, true} {
		var hdr http.Header
		var body string
		r := runExec(t, execOpts{nAcc: 1, claude: claude, streaming: streaming,
			up: func(_ int, req *http.Request, b string) (*http.Response, error) {
				hdr, body = req.Header.Clone(), b
				if streaming {
					return respBody(200, tfSSE(tfWrap(tfGood))), nil
				}
				return respBody(200, tfWrap(tfGood)), nil
			}})
		if !r.ok {
			t.Fatalf("stream=%v: status %d err %v", streaming, r.status, r.err)
		}
		if hdr.Get("User-Agent") != accounts.AntigravityUserAgent {
			t.Errorf("stream=%v: User-Agent = %q", streaming, hdr.Get("User-Agent"))
		}
		for _, h := range []string{"X-Goog-Api-Client", "Client-Metadata", "X-Goog-User-Project", "Origin", "Referer"} {
			if hdr.Get(h) != "" {
				t.Errorf("stream=%v: header %s must not be sent (%q)", streaming, h, hdr.Get(h))
			}
		}
		wantAccept := "application/json"
		if streaming {
			wantAccept = "text/event-stream"
		}
		if hdr.Get("Accept") != wantAccept {
			t.Errorf("stream=%v: Accept = %q, want %q", streaming, hdr.Get("Accept"), wantAccept)
		}
		for _, path := range []string{"request.safetySettings", "request.generationConfig.maxOutputTokens", "safetySettings"} {
			if gjson.Get(body, path).Exists() {
				t.Errorf("stream=%v: %s must not be sent: %s", streaming, path, gjson.Get(body, path).Raw)
			}
		}
		if gjson.Get(body, "request.system_instruction").Exists() || gjson.Get(body, "request.systemInstruction.parts.0.text").String() == "" {
			t.Errorf("stream=%v: the system instruction must be spelled systemInstruction on the wire: %.300s", streaming, body)
		}
		if gjson.Get(body, "userAgent").String() != "antigravity" ||
			!strings.HasPrefix(gjson.Get(body, "requestId").String(), "agent-") || gjson.Get(body, "request.sessionId").String() == "" {
			t.Errorf("stream=%v: the Antigravity envelope is incomplete: %.200s", streaming, body)
		}
		// requestType must be ABSENT (not "agent"): the real Antigravity IDE
		// client omits it on this path, and sending "agent" was found to
		// make Google bucket the request into a detail-free 429
		// RESOURCE_EXHAUSTED even with quota available.
		if gjson.Get(body, "requestType").Exists() {
			t.Errorf("stream=%v: requestType must be omitted, got %q", streaming, gjson.Get(body, "requestType").String())
		}
	}
}
