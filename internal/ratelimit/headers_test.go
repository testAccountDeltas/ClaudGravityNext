package ratelimit

import (
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/fakeids"
)

func gem(buckets ...accounts.QuotaBucket) *accounts.Record {
	return &accounts.Record{Email: "rl@example.com", Quotas: []accounts.QuotaGroup{{DisplayName: "Gemini Models", Buckets: buckets}}}
}

func bucket(window string, remaining float64, reset time.Time) accounts.QuotaBucket {
	return accounts.QuotaBucket{Window: window, RemainingFraction: remaining, ResetTime: reset.UTC().Format(time.RFC3339)}
}

func want(t *testing.T, h http.Header, k, v string) {
	t.Helper()
	if got := h.Get(k); got != v {
		t.Errorf("%s = %q, want %q", k, got, v)
	}
}

func TestBucketResetSec(t *testing.T) {
	now := time.Now()
	future := now.Add(3 * time.Hour)
	near := func(got int64, want time.Time) bool { d := got - want.Unix(); return d >= -2 && d <= 2 }

	if got := bucketResetSec(&accounts.QuotaBucket{ResetTime: future.UTC().Format(time.RFC3339)}, time.Hour); got != future.Unix() {
		t.Errorf("future reset must pass through: %d vs %d", got, future.Unix())
	}
	nano := future.UTC().Format(time.RFC3339Nano)
	if got := bucketResetSec(&accounts.QuotaBucket{ResetTime: nano}, time.Hour); got != future.Unix() {
		t.Errorf("RFC3339Nano reset: %d", got)
	}
	for name, b := range map[string]*accounts.QuotaBucket{
		"past":      {ResetTime: now.Add(-time.Hour).UTC().Format(time.RFC3339)},
		"30s ahead": {ResetTime: now.Add(30 * time.Second).UTC().Format(time.RFC3339)},
	} {
		if got := bucketResetSec(b, time.Hour); !near(got, now.Add(minResetAhead)) {
			t.Errorf("%s must clamp to now+%v, got %d", name, minResetAhead, got-now.Unix())
		}
	}
	for name, b := range map[string]*accounts.QuotaBucket{"nil": nil, "empty": {}, "malformed": {ResetTime: "soon"}} {
		if got := bucketResetSec(b, 5*time.Hour); !near(got, now.Add(5*time.Hour)) {
			t.Errorf("%s must use the fallback, got %d", name, got-now.Unix())
		}
	}
}

func TestSetSplitTokenRateLimitHeaders_80_20Split(t *testing.T) {
	h := http.Header{}
	reset := time.Date(2030, 5, 6, 7, 8, 9, 0, time.UTC)
	setSplitTokenRateLimitHeaders(h, 1000000, 0.25, reset)
	want(t, h, "anthropic-ratelimit-input-tokens-limit", "800000")
	want(t, h, "anthropic-ratelimit-input-tokens-remaining", "200000")
	want(t, h, "anthropic-ratelimit-input-tokens-reset", "2030-05-06T07:08:09Z")
	want(t, h, "anthropic-ratelimit-output-tokens-limit", "200000")
	want(t, h, "anthropic-ratelimit-output-tokens-remaining", "50000")
	want(t, h, "anthropic-ratelimit-output-tokens-reset", "2030-05-06T07:08:09Z")
}

func TestApplyHeaders_NilAccount_ExactSyntheticValues(t *testing.T) {
	h := http.Header{}
	ApplyHeaders(h, nil, "m")
	want(t, h, "anthropic-ratelimit-unified-5h-status", "allowed")
	want(t, h, "anthropic-ratelimit-unified-5h-utilization", "0.05")
	want(t, h, "anthropic-ratelimit-unified-7d-utilization", "0.05")
	want(t, h, "anthropic-ratelimit-unified-representative-claim", "five_hour")
	want(t, h, "anthropic-ratelimit-unified-overage-status", "rejected")
	want(t, h, "anthropic-ratelimit-requests-limit", "1000")
	want(t, h, "anthropic-ratelimit-requests-remaining", "950")
	want(t, h, "anthropic-ratelimit-tokens-remaining", "950000")
	want(t, h, "anthropic-ratelimit-input-tokens-limit", "800000")
	want(t, h, "anthropic-organization-id", fakeids.OrgID)
	want(t, h, "anthropic-workspace-id", fakeids.WorkspaceID)
	reset, err := strconv.ParseInt(h.Get("anthropic-ratelimit-unified-7d-reset"), 10, 64)
	if err != nil || reset < time.Now().Add(6*24*time.Hour).Unix() {
		t.Errorf("7d reset must be ~7 days ahead: %v %v", reset, err)
	}
}

func TestApplyHeaders_RealBuckets_ValuesAndRepresentativeWindow(t *testing.T) {
	now := time.Now()
	fiveReset, weekReset := now.Add(2*time.Hour), now.Add(100*time.Hour)
	// 5h: 25% left -> 0.75 used. weekly: 2% left -> 0.98 used (warning, more exhausted).
	acc := gem(bucket("5h", 0.25, fiveReset), bucket("weekly", 0.02, weekReset))
	h := http.Header{}
	ApplyHeaders(h, acc, "gemini-3-pro")

	want(t, h, "anthropic-ratelimit-unified-5h-utilization", "0.75")
	want(t, h, "anthropic-ratelimit-unified-5h-status", "allowed")
	want(t, h, "anthropic-ratelimit-unified-5h-reset", fmt.Sprint(fiveReset.Unix()))
	want(t, h, "anthropic-ratelimit-unified-7d-utilization", "0.98")
	want(t, h, "anthropic-ratelimit-unified-7d-status", "allowed_warning")
	want(t, h, "anthropic-ratelimit-unified-7d-reset", fmt.Sprint(weekReset.Unix()))
	want(t, h, "anthropic-ratelimit-unified-representative-claim", "seven_day")
	want(t, h, "anthropic-ratelimit-unified-status", "allowed_warning")
	want(t, h, "anthropic-ratelimit-unified-reset", fmt.Sprint(weekReset.Unix()))
	// Standard headers follow the 5h bucket.
	want(t, h, "anthropic-ratelimit-requests-remaining", "250")
	want(t, h, "anthropic-ratelimit-tokens-remaining", "250000")
	want(t, h, "anthropic-ratelimit-input-tokens-remaining", "200000")
	want(t, h, "anthropic-ratelimit-requests-reset", time.Unix(fiveReset.Unix(), 0).UTC().Format(time.RFC3339))
	want(t, h, "anthropic-ratelimit-unified-overage-disabled-reason", "org_level_disabled")
	want(t, h, "anthropic-organization-id", fakeids.OrgID)
}

func TestApplyHeaders_ExhaustedBucketIsRejected(t *testing.T) {
	h := http.Header{}
	ApplyHeaders(h, gem(bucket("5h", 0, time.Now().Add(time.Hour)), bucket("weekly", 0.9, time.Now().Add(50*time.Hour))), "m")
	want(t, h, "anthropic-ratelimit-unified-5h-status", "rejected")
	want(t, h, "anthropic-ratelimit-unified-5h-utilization", "1.00")
	want(t, h, "anthropic-ratelimit-unified-status", "rejected")
	want(t, h, "anthropic-ratelimit-unified-representative-claim", "five_hour")
	want(t, h, "anthropic-ratelimit-requests-remaining", "0")
}

func TestApplyHeaders_TieKeepsFiveHourRepresentative(t *testing.T) {
	h := http.Header{}
	ApplyHeaders(h, gem(bucket("5h", 0.5, time.Now().Add(time.Hour)), bucket("weekly", 0.5, time.Now().Add(50*time.Hour))), "m")
	want(t, h, "anthropic-ratelimit-unified-representative-claim", "five_hour")
}

func TestApplyHeaders_OnlyWeeklyBucket(t *testing.T) {
	weekReset := time.Now().Add(60 * time.Hour)
	h := http.Header{}
	ApplyHeaders(h, gem(bucket("weekly", 0.25, weekReset)), "m")
	want(t, h, "anthropic-ratelimit-unified-5h-status", "allowed")
	want(t, h, "anthropic-ratelimit-unified-5h-utilization", "0.05")
	want(t, h, "anthropic-ratelimit-unified-7d-utilization", "0.75")
	want(t, h, "anthropic-ratelimit-unified-representative-claim", "seven_day")
	want(t, h, "anthropic-ratelimit-requests-remaining", "250") // falls back to the weekly numbers
	want(t, h, "anthropic-ratelimit-requests-reset", time.Unix(weekReset.Unix(), 0).UTC().Format(time.RFC3339))
}

func TestApplyHeaders_QuotasWithoutKnownWindowsGetDefaults(t *testing.T) {
	h := http.Header{}
	ApplyHeaders(h, gem(bucket("hourly", 0.1, time.Now().Add(time.Hour))), "m")
	want(t, h, "anthropic-ratelimit-unified-5h-utilization", "0.05")
	want(t, h, "anthropic-ratelimit-unified-7d-utilization", "0.05")
	want(t, h, "anthropic-ratelimit-requests-remaining", "950")
}

func TestApplyHeaders_UnknownFractionReportsNoUsage(t *testing.T) {
	b := bucket("5h", 0, time.Now().Add(time.Hour))
	b.FractionUnknown = true
	h := http.Header{}
	ApplyHeaders(h, gem(b), "m")
	want(t, h, "anthropic-ratelimit-unified-5h-utilization", "0.00")
	want(t, h, "anthropic-ratelimit-unified-5h-status", "allowed")
}

func TestIsNative3PModelExhausted_UnknownFractionIsNotExhausted(t *testing.T) {
	b := accounts.QuotaBucket{Window: "5h", FractionUnknown: true}
	acc := &accounts.Record{Quotas: []accounts.QuotaGroup{{DisplayName: "Claude and GPT", Buckets: []accounts.QuotaBucket{b}}}}
	if IsNative3PModelExhausted(acc) {
		t.Fatal("a bucket without a fraction must not read as exhausted")
	}
}

func TestPickFiveHourAndWeekly_GPTPrefixTargetsClaudeGroup(t *testing.T) {
	acc := &accounts.Record{Quotas: []accounts.QuotaGroup{
		{DisplayName: "Gemini Models", Buckets: []accounts.QuotaBucket{{Window: "5h", BucketID: "g"}}},
		{DisplayName: "Claude and GPT", Buckets: []accounts.QuotaBucket{{Window: "5h", BucketID: "c"}, {Window: "weekly", BucketID: "cw"}}},
	}}
	fh, wk := pickFiveHourAndWeekly(acc, "gpt-oss-120b")
	if fh == nil || fh.BucketID != "c" || wk == nil || wk.BucketID != "cw" {
		t.Fatalf("got %+v %+v", fh, wk)
	}
	fh, _ = pickFiveHourAndWeekly(acc, "gemini-3-pro")
	if fh.BucketID != "g" {
		t.Fatalf("gemini model must use the Gemini group, got %+v", fh)
	}
	if a, b := pickFiveHourAndWeekly(nil, "m"); a != nil || b != nil {
		t.Fatal("nil account")
	}
}
