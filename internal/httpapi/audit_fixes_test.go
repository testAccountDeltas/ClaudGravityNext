package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleCacheTouch_SucceedsOnRouter(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	router := NewRouter(cm)

	for _, p := range []string{"/v1/messages/cache_touch", "/v1/messages/cache_touch/"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, p, nil)
		r.Header.Set("x-api-key", "sk-good")
		router.ServeHTTP(w, r)

		if w.Code != http.StatusOK {
			t.Fatalf("POST %s status = %d, want %d", p, w.Code, http.StatusOK)
		}
		var resp map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if resp["status"] != "ok" || resp["touched"] != true {
			t.Errorf("resp = %v, want status:ok, touched:true", resp)
		}
	}
}

func TestHandleUsage_RequiresAuth(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/usage", nil)
	handleUsage(cm, w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d (unauthenticated /v1/usage must be rejected)", w.Code, http.StatusUnauthorized)
	}
}

func TestHandleUsage_MethodNotAllowed(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/usage", nil)
	r.Header.Set("x-api-key", "sk-good")
	handleUsage(cm, w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestWriteAnthropicError_529SetsRepresentativeClaimAndOverageHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	WriteAnthropicError(w, 529, "overloaded_error", "Overloaded")

	if w.Code != 529 {
		t.Fatalf("status = %d, want 529", w.Code)
	}
	if claim := w.Header().Get("anthropic-ratelimit-unified-representative-claim"); claim != "five_hour" {
		t.Errorf("representative-claim = %q, want five_hour", claim)
	}
	if overage := w.Header().Get("anthropic-ratelimit-unified-overage-status"); overage != "rejected" {
		t.Errorf("overage-status = %q, want rejected", overage)
	}
	if reason := w.Header().Get("anthropic-ratelimit-unified-overage-disabled-reason"); reason != "org_level_disabled" {
		t.Errorf("overage-disabled-reason = %q, want org_level_disabled", reason)
	}
}

func TestCommonAPIMiddleware_PreservesIncomingRequestID(t *testing.T) {
	handler := CommonAPIMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/test", nil)
	r.Header.Set("x-request-id", "client-trace-12345")
	handler.ServeHTTP(w, r)

	if rid := w.Header().Get("request-id"); rid != "client-trace-12345" {
		t.Errorf("request-id = %q, want client-trace-12345", rid)
	}
	if xrid := w.Header().Get("x-request-id"); xrid != "client-trace-12345" {
		t.Errorf("x-request-id = %q, want client-trace-12345", xrid)
	}
}

// gemini-4-* ids are listed on Google's public deprecations page but are not
// served through Cloud Code (Antigravity), so the model list must not
// advertise them: a client picking one only gets a 404 from Google. (A direct
// lookup of an unknown gemini-* id still resolves through lookupModel's
// family fallback, by design.)
func TestAllModels_DoesNotAdvertiseGemini4(t *testing.T) {
	for _, m := range allModels() {
		if id, _ := m["id"].(string); strings.HasPrefix(id, "gemini-4") {
			t.Errorf("model list advertises %q, which Cloud Code does not serve", id)
		}
	}
}

func TestHandleEmbeddings_DefaultIsGeminiEmbedding001(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	router := NewRouter(cm)

	body := `{"input":"test embedding default model"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	r.Header.Set("x-api-key", "sk-good")
	router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body = %s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Model != "gemini-embedding-001" {
		t.Errorf("model = %q, want gemini-embedding-001", resp.Model)
	}
}

func TestHandleMessages_BodyExactLimitAndPlusOne(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")

	// Exactly 25MB limit (maxRequestBodyBytes = 25*1024*1024 = 26214400)
	// Body of exactly 25MB: MaxBytesReader should NOT trigger "request too large".
	exactBody := strings.Repeat(" ", maxRequestBodyBytes)
	wExact := httptest.NewRecorder()
	rExact := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(exactBody))
	rExact.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, wExact, rExact)

	// It should reach validation (and fail because empty/whitespace is not valid JSON, returning 400, NOT 413)
	if wExact.Code != http.StatusBadRequest {
		t.Fatalf("exact 25MB status = %d, want %d (should reach validation, not 413)", wExact.Code, http.StatusBadRequest)
	}

	// Exactly 25MB + 1 byte: MaxBytesReader MUST trigger 413 request_too_large
	overBody := strings.Repeat(" ", maxRequestBodyBytes+1)
	wOver := httptest.NewRecorder()
	rOver := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(overBody))
	rOver.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, wOver, rOver)

	if wOver.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("25MB+1 status = %d, want %d", wOver.Code, http.StatusRequestEntityTooLarge)
	}
	var errResp map[string]interface{}
	if err := json.Unmarshal(wOver.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("unmarshal 413 response: %v", err)
	}
	errObj, _ := errResp["error"].(map[string]interface{})
	if errObj["type"] != "request_too_large" {
		t.Errorf("error type = %v, want request_too_large", errObj["type"])
	}
}

func TestHandleMessages_PostTruncationInlineDataLimit(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")

	// Create a valid JSON request whose base64 image data exceeds 20MB (e.g. 21MB)
	// Base64 chars: 21 * 1024 * 1024
	hugeBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB" + strings.Repeat("A", 21*1024*1024) // valid PNG magic, then padding
	body := `{
				"model": "claude-sonnet-4-6",
				"max_tokens": 100,
				"messages": [
					{
						"role": "user",
						"content": [
							{
								"type": "image",
								"source": {
									"type": "base64",
									"media_type": "image/png",
									"data": "` + hugeBase64 + `"
								}
							}
						]
					}
				]
			}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, w, r)

	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
	var errResp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &errResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errObj, _ := errResp["error"].(map[string]interface{})
	if errObj["type"] != "request_too_large" {
		t.Errorf("error type = %v, want request_too_large", errObj["type"])
	}
	if !strings.Contains(errObj["message"].(string), "20MB") {
		t.Errorf("message = %v, want it to mention 20MB limit", errObj["message"])
	}
}

func TestHandleMessages_CitationsInRequestBody_Survives(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")

	body := `{
				"model": "claude-sonnet-4-6",
				"max_tokens": 100,
				"messages": [
					{
						"role": "user",
						"content": [
							{
								"type": "text",
								"text": "According to the document, the Earth orbits the Sun.",
								"citations": [
									{
										"type": "char_location",
										"cited_text": "the Earth orbits the Sun",
										"document_index": 0,
										"document_title": "astronomy.pdf",
										"start_char_index": 0,
										"end_char_index": 24
									}
								]
							}
						]
					}
				]
			}`

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, w, r)

	// In test config with dummy api key, it should pass validation and translation
	// and attempt orchestrate.Execute (which fails upstream with 502/500, but NOT 400 invalid_request_error).
	if w.Code == http.StatusBadRequest {
		t.Fatalf("status = %d, citations must not fail validation with 400: %s", w.Code, w.Body.String())
	}
}

func TestHandleMessages_ClientDisconnect_CancelsContext(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately to simulate a client closing connection before first byte
	cancel()

	body := `{"model":"claude-sonnet-4-6","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("x-api-key", "sk-good")
	handleMessages(cm, w, r)

	// Since context is cancelled, orchestrate fails with context.Canceled / 499.
	// Must not hang, panic, or crash.
	if w.Code == 200 {
		t.Errorf("cancelled request should not return 200 OK")
	}
}
