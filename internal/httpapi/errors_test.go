package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteAnthropicError_429SetsUnifiedResetAndRetryAfter(t *testing.T) {
	w := httptest.NewRecorder()
	WriteAnthropicError(w, http.StatusTooManyRequests, "rate_limit_error", "slow down")

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}
	if got := w.Header().Get("Retry-After"); got != "900" {
		t.Errorf("Retry-After = %q, want 900", got)
	}
	if got := w.Header().Get("anthropic-ratelimit-unified-status"); got != "rejected" {
		t.Errorf("anthropic-ratelimit-unified-status = %q, want rejected", got)
	}
	if got := w.Header().Get("anthropic-ratelimit-unified-reset"); got == "" {
		t.Error("anthropic-ratelimit-unified-reset is empty, want a future unix timestamp")
	}
	if got := w.Header().Get("anthropic-ratelimit-requests-remaining"); got != "0" {
		t.Errorf("anthropic-ratelimit-requests-remaining = %q, want 0", got)
	}
	if got := w.Header().Get("anthropic-ratelimit-input-tokens-limit"); got == "" {
		t.Error("anthropic-ratelimit-input-tokens-limit is empty, want the 80%% split set")
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

func TestWriteAnthropicError_OverloadedWritesWireStatus529(t *testing.T) {
	w := httptest.NewRecorder()
	// The real Anthropic API answers an overloaded backend with HTTP 529,
	// not whatever Google's own upstream status (502/503/504) was —
	// passing that status through verbatim leaves this 529-specific
	// client retry path unreachable.
	WriteAnthropicError(w, http.StatusBadGateway, "overloaded_error", "upstream overloaded")

	if w.Code != 529 {
		t.Fatalf("wire status = %d, want 529", w.Code)
	}
	if got := w.Header().Get("Retry-After"); got != "3" {
		t.Errorf("Retry-After = %q, want 3", got)
	}
	// GAP regression: a Round-14 audit caught that a 529 reported through
	// this path got only Retry-After, missing the SAME
	// anthropic-ratelimit-unified-reset/*-limit/*-remaining header set a
	// 429 already carried — a client reading those headers (not just the
	// status code) to drive its backoff/countdown display saw them
	// entirely absent on an overload, not just empty.
	for _, h := range []string{
		"anthropic-ratelimit-unified-reset",
		"anthropic-ratelimit-unified-status",
		"anthropic-ratelimit-requests-limit",
		"anthropic-ratelimit-requests-remaining",
		"anthropic-ratelimit-tokens-limit",
		"anthropic-ratelimit-tokens-remaining",
	} {
		if w.Header().Get(h) == "" {
			t.Errorf("529 response missing header %q, want it set same as a 429", h)
		}
	}
}

func TestWriteAnthropicError_BodyShape(t *testing.T) {
	w := httptest.NewRecorder()
	WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "bad input")
	body := w.Body.String()
	want := `{"error":{"message":"bad input","type":"invalid_request_error"},"type":"error"}`
	if body != want {
		t.Fatalf("body = %s, want %s", body, want)
	}
	if got := w.Header().Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
	if got := w.Header().Get("request-id"); got == "" {
		t.Error("request-id missing on error response")
	}
	if got := w.Header().Get("x-request-id"); got == "" {
		t.Error("x-request-id missing on error response")
	}
}
