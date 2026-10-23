package ratelimit

import (
	"net/http/httptest"
	"testing"

	"claudgravity/internal/accounts"
)

func TestIsNative3PModelExhausted_NilAccountIsFalse(t *testing.T) {
	if IsNative3PModelExhausted(nil) {
		t.Fatal("expected false for a nil account")
	}
}

func TestIsNative3PModelExhausted_NoClaudeGroupButQuotasProbed(t *testing.T) {
	// A real bug fix: quotas were probed but came back with no Claude/GPT
	// group at all — on a free-tier account that means 3P was never
	// provisioned, not "quota unknown, assume available".
	acc := &accounts.Record{Quotas: []accounts.QuotaGroup{
		{DisplayName: "Gemini Models", Buckets: []accounts.QuotaBucket{{RemainingFraction: 1.0}}},
	}}
	if !IsNative3PModelExhausted(acc) {
		t.Fatal("expected exhausted=true when quotas exist but no Claude/GPT group is present")
	}
}

func TestIsNative3PModelExhausted_ClaudeGroupWithRemainingQuota(t *testing.T) {
	acc := &accounts.Record{Quotas: []accounts.QuotaGroup{
		{DisplayName: "Claude Models", Buckets: []accounts.QuotaBucket{{RemainingFraction: 0.5}}},
	}}
	if IsNative3PModelExhausted(acc) {
		t.Fatal("expected not-exhausted when a Claude bucket still has >5% remaining")
	}
}

func TestIsNative3PModelExhausted_ClaudeGroupBelowFivePercent(t *testing.T) {
	acc := &accounts.Record{Quotas: []accounts.QuotaGroup{
		{DisplayName: "Claude Models", Buckets: []accounts.QuotaBucket{{RemainingFraction: 0.01}}},
	}}
	if !IsNative3PModelExhausted(acc) {
		t.Fatal("expected exhausted when every Claude bucket is at or below the 5% floor")
	}
}

func TestBucketFraction_NilIsZero(t *testing.T) {
	if f := bucketFraction(nil); f != 0 {
		t.Fatalf("expected 0 for nil bucket, got %v", f)
	}
}

func TestBucketFraction_ClampedToUnitRange(t *testing.T) {
	if f := bucketFraction(&accounts.QuotaBucket{RemainingFraction: -0.5}); f != 1 {
		t.Fatalf("expected clamp to 1 for an out-of-range remaining fraction, got %v", f)
	}
	if f := bucketFraction(&accounts.QuotaBucket{RemainingFraction: 1.5}); f != 0 {
		t.Fatalf("expected clamp to 0, got %v", f)
	}
}

func TestPickFiveHourAndWeekly_PrefersClaudeGroupForClaudeModel(t *testing.T) {
	acc := &accounts.Record{Quotas: []accounts.QuotaGroup{
		{DisplayName: "Gemini Models", Buckets: []accounts.QuotaBucket{{Window: "5h", RemainingFraction: 0.9}}},
		{DisplayName: "Claude Models", Buckets: []accounts.QuotaBucket{{Window: "5h", RemainingFraction: 0.1}}},
	}}
	fh, _ := pickFiveHourAndWeekly(acc, "claude-opus-5-5")
	if fh == nil || fh.RemainingFraction != 0.1 {
		t.Fatalf("expected the Claude group's bucket to be picked for a claude- model, got %v", fh)
	}
}

func TestPickFiveHourAndWeekly_FallsBackToFirstGroupWithBuckets(t *testing.T) {
	acc := &accounts.Record{Quotas: []accounts.QuotaGroup{
		{DisplayName: "Some Other Group", Buckets: []accounts.QuotaBucket{{Window: "7d", RemainingFraction: 0.4}}},
	}}
	fh, wk := pickFiveHourAndWeekly(acc, "gemini-3.8-flash-tiered")
	if fh != nil {
		t.Fatalf("expected no five-hour bucket, got %v", fh)
	}
	if wk == nil || wk.RemainingFraction != 0.4 {
		t.Fatalf("expected the fallback group's weekly bucket to be picked, got %v", wk)
	}
}

func TestIsFiveHourWindow_RecognizesSeparatorVariants(t *testing.T) {
	for _, w := range []string{"5h", "5_hour", "5-hour", "18000"} {
		if !isFiveHourWindow(w) {
			t.Fatalf("expected %q to be recognized as a five-hour window", w)
		}
	}
}

func TestIsWeeklyWindow_RecognizesSeparatorVariants(t *testing.T) {
	for _, w := range []string{"week", "7d", "7_day", "7-day"} {
		if !isWeeklyWindow(w) {
			t.Fatalf("expected %q to be recognized as a weekly window", w)
		}
	}
}

func TestApplyHeaders_NilAccountSetsSyntheticUnifiedHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	ApplyHeaders(w.Header(), nil, "gemini-3.8-flash-tiered")
	h := w.Header()
	if h.Get("anthropic-ratelimit-unified-status") != "allowed" {
		t.Fatalf("expected a synthetic allowed status for a nil account (api_key tenant), got %q", h.Get("anthropic-ratelimit-unified-status"))
	}
	if h.Get("anthropic-ratelimit-tokens-limit") == "" {
		t.Fatal("expected the standard per-window headers to be set too, not just the unified-* ones")
	}
}
