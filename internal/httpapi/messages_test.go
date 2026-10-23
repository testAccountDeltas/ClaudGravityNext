package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestHandleMessages_MissingAPIKeyIs401WithAnthropicShape(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	body := `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	handleMessages(cm, w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"authentication_error"`) {
		t.Errorf("body = %s, want authentication_error type", w.Body.String())
	}
}

func TestHandleMessages_EmptyOrWhitespaceKeyIs401(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	body := `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`

	for _, headerVal := range []string{"", "   ", "Bearer ", "Bearer    "} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		if strings.HasPrefix(headerVal, "Bearer") {
			r.Header.Set("Authorization", headerVal)
		} else {
			r.Header.Set("x-api-key", headerVal)
		}
		handleMessages(cm, w, r)

		if w.Code != http.StatusUnauthorized {
			t.Fatalf("key %q: status = %d, want %d", headerVal, w.Code, http.StatusUnauthorized)
		}
	}
}

func TestHandleMessages_MalformedJSONIs400(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":`))
	r.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}

func TestHandleMessages_MissingMaxTokensIs400(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "max_tokens") {
		t.Errorf("body = %s, want it to mention max_tokens", w.Body.String())
	}
}

func TestHandleMessages_NegativeMaxTokensIs400(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	body := `{"model":"m","max_tokens":-1,"messages":[{"role":"user","content":"hi"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "max_tokens") {
		t.Errorf("body = %s, want it to mention max_tokens", w.Body.String())
	}
}

func TestHandleMessages_BodyOverCapIs413(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	// One text block padded past the 25MB cap — the oversized payload
	// itself doesn't need to be valid JSON past the cap check, since
	// http.MaxBytesReader errors out of io.ReadAll before validation
	// ever runs.
	huge := strings.Repeat("a", maxRequestBodyBytes+1024)
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(huge))
	r.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, w, r)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
	if !strings.Contains(w.Body.String(), `"request_too_large"`) {
		t.Errorf("body = %s, want request_too_large type", w.Body.String())
	}
}

func TestHandleMessages_QuotaProbeFastPathSkipsTranslation(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	body := `{"model":"claude-opus-4-5","max_tokens":1,"messages":[{"role":"user","content":"quota"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"msg_probe"`) {
		t.Errorf("body = %s, want the stable synthetic probe response", w.Body.String())
	}
	// The probe must still carry real rate-limit headers (from the
	// sticky account's own quota, or the api_key-tenant synthetic
	// fallback) — a client reading its own quota display off a probe
	// response needs these just as much as off a real one.
	if w.Header().Get("anthropic-ratelimit-unified-status") == "" {
		t.Error("anthropic-ratelimit-unified-status missing on the quota-probe fast path")
	}
}

func TestHandleMessages_ArrayShapedQuotaProbeAlsoMatches(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	// The SDK can serialize a single-text-block message's content as
	// the block-array shape instead of a bare string.
	body := `{"model":"claude-opus-4-5","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"quota"}]}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
}

func TestHandleMessages_StreamingQuotaProbeReturnsValidSSE(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	body := `{"model":"claude-opus-4-5","max_tokens":1,"stream":true,"messages":[{"role":"user","content":"quota"}]}`
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	contentType := w.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", contentType)
	}
	if w.Header().Get("anthropic-ratelimit-unified-status") == "" {
		t.Error("anthropic-ratelimit-unified-status missing on streaming quota-probe fast path")
	}

	bodyStr := w.Body.String()
	requiredEvents := []string{
		"event: message_start",
		"event: content_block_start",
		"event: content_block_delta",
		"event: content_block_stop",
		"event: message_delta",
		"event: message_stop",
	}
	for _, ev := range requiredEvents {
		if !strings.Contains(bodyStr, ev) {
			t.Errorf("missing event %q in SSE stream output: %s", ev, bodyStr)
		}
	}
	if !strings.Contains(bodyStr, `"msg_probe"`) {
		t.Errorf("missing msg_probe in body: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"claude-opus-4-5"`) {
		t.Errorf("missing model claude-opus-4-5 in body: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"text_delta"`) || !strings.Contains(bodyStr, `"ok"`) {
		t.Errorf("missing text delta ok in body: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"max_tokens"`) {
		t.Errorf("missing stop_reason max_tokens in body: %s", bodyStr)
	}
}

func TestIsQuotaProbeRequest_RejectsNonProbeShapes(t *testing.T) {
	cases := []string{
		`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"not quota"}]}`,
		`{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"quota"}]}`,                                                     // wrong max_tokens
		`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"quota"},{"role":"assistant","content":"ok"}]}`,                 // more than one message
		`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"quota"},{"type":"text","text":"x"}]}]}`, // more than one block
	}
	for _, body := range cases {
		if isQuotaProbeRequest([]byte(body)) {
			t.Errorf("isQuotaProbeRequest(%s) = true, want false", body)
		}
	}
}

func TestNewRouter_MessagesMethodNotAllowedSetsAllowHeader(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	router := NewRouter(cm)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/messages", nil)
	router.ServeHTTP(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
	if got := w.Header().Get("Allow"); got != "POST" {
		t.Errorf("Allow = %q, want POST", got)
	}
}

func TestNewRouter_UnknownV1PathIs404(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	router := NewRouter(cm)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/not-a-real-endpoint", nil)
	router.ServeHTTP(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func TestMergeAnthropicBetaHeader(t *testing.T) {
	t.Run("merges into body without betas", func(t *testing.T) {
		body := []byte(`{"model":"claude-3-7-sonnet-20250219","max_tokens":100}`)
		header := " fast-mode-2026-02-01 , task-budgets-2026-03-13 "
		updated := mergeAnthropicBetaHeader(body, header)

		betas := gjson.GetBytes(updated, "betas").Array()
		if len(betas) != 2 {
			t.Fatalf("len(betas) = %d, want 2", len(betas))
		}
		if betas[0].String() != "fast-mode-2026-02-01" {
			t.Errorf("betas[0] = %q, want fast-mode-2026-02-01", betas[0].String())
		}
		if betas[1].String() != "task-budgets-2026-03-13" {
			t.Errorf("betas[1] = %q, want task-budgets-2026-03-13", betas[1].String())
		}
	})

	t.Run("merges and deduplicates with existing body betas", func(t *testing.T) {
		body := []byte(`{"model":"m","max_tokens":100,"betas":["existing-feature","fast-mode-2026-02-01"]}`)
		header := "fast-mode-2026-02-01, task-budgets-2026-03-13"
		updated := mergeAnthropicBetaHeader(body, header)

		betas := gjson.GetBytes(updated, "betas").Array()
		if len(betas) != 3 {
			t.Fatalf("len(betas) = %d, want 3", len(betas))
		}
		var list []string
		for _, b := range betas {
			list = append(list, b.String())
		}
		expected := []string{"existing-feature", "fast-mode-2026-02-01", "task-budgets-2026-03-13"}
		for i, exp := range expected {
			if list[i] != exp {
				t.Errorf("betas[%d] = %q, want %q", i, list[i], exp)
			}
		}
	})

	t.Run("empty header leaves body unchanged", func(t *testing.T) {
		body := []byte(`{"model":"m","max_tokens":100}`)
		updated := mergeAnthropicBetaHeader(body, "")
		if string(updated) != string(body) {
			t.Errorf("body modified unexpectedly: %s", string(updated))
		}
	})
}
