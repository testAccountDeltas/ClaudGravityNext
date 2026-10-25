package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"claudgravity/internal/accounts"
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
	want := `{"error":{"message":"bad input","type":"invalid_request_error"},"request_id":"` + w.Header().Get("request-id") + `","type":"error"}`
	if body != want {
		t.Fatalf("body = %s, want %s", body, want)
	}
	if got := w.Header().Get("x-should-retry"); got != "false" {
		t.Errorf("x-should-retry on 400 = %q, want false", got)
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

func TestWriteAnthropicError_ShouldRetryHeader(t *testing.T) {
	for status, want := range map[int]string{500: "true", 502: "true", 529: "true", 429: "true", 400: "false", 401: "false", 404: "false"} {
		w := httptest.NewRecorder()
		WriteAnthropicError(w, status, "api_error", "x")
		if got := w.Header().Get("x-should-retry"); got != want {
			t.Errorf("status %d: x-should-retry = %q, want %q", status, got, want)
		}
	}
}

func TestClientUpstreamMessage(t *testing.T) {
	cases := []struct {
		status int
		in     string
		want   string
	}{
		{502, "upstream failed after retries: upstream failed after retries: no accounts available in pool", "no accounts available in pool"},
		{429, `upstream failed after retries: status 429: {"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`, "Rate limited by the upstream provider. Please retry later."},
		{503, "Upstream failed after retries: [{\"error\": 1}]", "The upstream provider is temporarily unavailable. Please retry."},
		{500, "", "Upstream request failed. Please retry."},
	}
	for _, c := range cases {
		got := ClientUpstreamMessage(c.status, c.in)
		if got != c.want {
			t.Errorf("ClientUpstreamMessage(%d, %q) = %q, want %q", c.status, c.in, got, c.want)
		}
		if strings.Contains(strings.ToLower(got), "after retries") || strings.ContainsAny(got, "{}") {
			t.Errorf("message leaks wrapper/JSON: %q", got)
		}
	}
}

func TestRetryAfterFromPoolCooldown(t *testing.T) {
	now := time.Now()
	rec := func(until time.Duration, disabled bool) *accounts.Record {
		return &accounts.Record{CooldownUntil: now.Add(until), Disabled: disabled}
	}
	if d := retryAfterFromRecords(nil, now); d != 900*time.Second {
		t.Errorf("empty pool = %v", d)
	}
	// Soonest cooldown among enabled accounts wins; disabled ones are ignored.
	if d := retryAfterFromRecords([]*accounts.Record{rec(600*time.Second, false), rec(120*time.Second, false), rec(5*time.Second, true)}, now); d != 120*time.Second {
		t.Errorf("soonest cooldown = %v, want 2m", d)
	}
	// Clamped to 15 minutes.
	if d := retryAfterFromRecords([]*accounts.Record{rec(5*time.Hour, false)}, now); d != 900*time.Second {
		t.Errorf("clamp = %v", d)
	}
	// An account usable now -> short retry.
	if d := retryAfterFromRecords([]*accounts.Record{rec(120*time.Second, false), {}}, now); d != 30*time.Second {
		t.Errorf("idle account = %v", d)
	}
}

// An account with no cooldown but an exhausted quota bucket is not idle: the
// client must be told to wait for the bucket reset, not 30 seconds.
func TestRetryAfterCountsExhaustedQuotaBuckets(t *testing.T) {
	now := time.Now()
	exhausted := &accounts.Record{Quotas: []accounts.QuotaGroup{{
		DisplayName: "Gemini Models",
		Buckets:     []accounts.QuotaBucket{{BucketID: "5h", RemainingFraction: 0, ResetTime: now.Add(2 * time.Hour).Format(time.RFC3339)}},
	}}}
	if d := retryAfterFromRecords([]*accounts.Record{exhausted}, now); d != 900*time.Second {
		t.Errorf("exhausted bucket = %v, want the 15 minute clamp", d)
	}
	soon := &accounts.Record{Quotas: []accounts.QuotaGroup{{
		DisplayName: "Gemini Models",
		Buckets:     []accounts.QuotaBucket{{BucketID: "5h", RemainingFraction: 0, ResetTime: now.Add(3 * time.Minute).Format(time.RFC3339)}},
	}}}
	if d := retryAfterFromRecords([]*accounts.Record{exhausted, soon}, now); d < 170*time.Second || d > 181*time.Second {
		t.Errorf("soonest reset = %v, want about 3m", d)
	}
	fresh := &accounts.Record{Quotas: []accounts.QuotaGroup{{
		DisplayName: "Gemini Models",
		Buckets:     []accounts.QuotaBucket{{BucketID: "5h", RemainingFraction: 0.8, ResetTime: now.Add(time.Hour).Format(time.RFC3339)}},
	}}}
	if d := retryAfterFromRecords([]*accounts.Record{exhausted, fresh}, now); d != 30*time.Second {
		t.Errorf("a healthy account = %v, want 30s", d)
	}
}
