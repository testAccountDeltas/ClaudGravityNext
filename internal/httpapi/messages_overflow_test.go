package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A prompt far over the model window is rejected up front with Anthropic's
// exact auto-compaction wording, not forwarded to Gemini or silently cut.
func TestHandleMessages_OversizedPromptIsAnthropicPromptTooLong(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	var sb strings.Builder
	sb.WriteString(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[`)
	for i := 0; i < 20; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"role":"` + role + `","content":"` + strings.Repeat("lorem ipsum dolor sit amet ", 40_000) + `"}`)
	}
	sb.WriteString(`,{"role":"user","content":"q"}]}`)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(sb.String()))
	r.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error.Type != "invalid_request_error" || !strings.HasPrefix(resp.Error.Message, "prompt is too long: ") || !strings.HasSuffix(resp.Error.Message, " maximum") {
		t.Fatalf("unexpected error: %+v", resp)
	}
	if strings.Contains(resp.Error.Message, "Upstream failed") || strings.Contains(resp.Error.Message, "{") {
		t.Fatalf("raw upstream text leaked: %s", resp.Error.Message)
	}
}
