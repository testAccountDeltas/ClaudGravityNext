// Characterization test for warmup.go's needsGeminiWeeklyWarmup — ported
// verbatim from superrelay (v1)'s warmup.go, closing the deferral
// documented in that file's doc comment and ROADMAP_PROGRESS.md's Phase
// 1 entry for ui.go/warmup.go. WarmupSingleAccount/WarmupAllFreshAccounts
// themselves make real HTTP calls (RefreshAccessToken/the warmup probe
// itself), so — same as every other network-touching accounts method in
// this package — they're exercised live via cmd/gateway, not unit-tested
// against a fake Google here.
package accounts

import (
	"context"
	"testing"
)

func TestNeedsGeminiWeeklyWarmup_FreshWeeklyBucketNeedsWarmup(t *testing.T) {
	acc := &Record{
		Email: "fresh@example.com",
		Quotas: []QuotaGroup{
			{DisplayName: "Gemini", Buckets: []QuotaBucket{
				{Window: "week", RemainingFraction: 1.0},
			}},
		},
	}
	if !needsGeminiWeeklyWarmup(acc) {
		t.Fatal("a Gemini weekly bucket sitting at 100% remaining must be reported as needing warmup")
	}
}

func TestNeedsGeminiWeeklyWarmup_ActivatedAccountDoesNotNeedWarmup(t *testing.T) {
	acc := &Record{
		Email: "active@example.com",
		Quotas: []QuotaGroup{
			{DisplayName: "Gemini", Buckets: []QuotaBucket{
				{Window: "week", RemainingFraction: 0.87},
			}},
		},
	}
	if needsGeminiWeeklyWarmup(acc) {
		t.Fatal("an account whose weekly bucket has already moved off 100% must not be reported as needing warmup")
	}
}

func TestNeedsGeminiWeeklyWarmup_NonGeminiGroupIgnored(t *testing.T) {
	acc := &Record{
		Email: "claude-only@example.com",
		Quotas: []QuotaGroup{
			{DisplayName: "Claude", Buckets: []QuotaBucket{
				{Window: "week", RemainingFraction: 1.0},
			}},
		},
	}
	if needsGeminiWeeklyWarmup(acc) {
		t.Fatal("a fresh weekly bucket in a non-Gemini quota group must not trigger warmup")
	}
}

func TestNeedsGeminiWeeklyWarmup_NonWeeklyWindowIgnored(t *testing.T) {
	acc := &Record{
		Email: "5h-only@example.com",
		Quotas: []QuotaGroup{
			{DisplayName: "Gemini", Buckets: []QuotaBucket{
				{Window: "5h", RemainingFraction: 1.0},
			}},
		},
	}
	if needsGeminiWeeklyWarmup(acc) {
		t.Fatal("a fresh 5h bucket must not trigger warmup — only the weekly window is checked")
	}
}

func TestWarmupAllFreshAccounts_SkipsDisabledAndActivatedAccounts(t *testing.T) {
	fresh := &Record{
		Email:    "needs-warmup@example.com",
		Disabled: false,
		Quotas: []QuotaGroup{
			{DisplayName: "Gemini", Buckets: []QuotaBucket{{Window: "week", RemainingFraction: 1.0}}},
		},
	}
	disabledButFresh := &Record{
		Email:    "disabled-fresh@example.com",
		Disabled: true,
		Quotas: []QuotaGroup{
			{DisplayName: "Gemini", Buckets: []QuotaBucket{{Window: "week", RemainingFraction: 1.0}}},
		},
	}
	alreadyActivated := &Record{
		Email:    "already-active@example.com",
		Disabled: false,
		Quotas: []QuotaGroup{
			{DisplayName: "Gemini", Buckets: []QuotaBucket{{Window: "week", RemainingFraction: 0.5}}},
		},
	}
	seedAccounts(t, fresh, disabledButFresh, alreadyActivated)

	// WarmupSingleAccount will fail fast here (no RefreshToken set, so
	// RefreshAccessToken errors immediately without any real network
	// call) — this test's job is only to prove the FILTER
	// (needsGeminiWeeklyWarmup + !Disabled) decides which accounts
	// WarmupAllFreshAccounts even attempts, via the returned errs map's
	// key set, not to prove the warmup probe itself succeeds.
	_, errs := globalAccountManager.WarmupAllFreshAccounts(context.Background())

	if _, ok := errs[fresh.Email]; !ok {
		t.Errorf("expected an attempt (and thus an error entry, no refresh token set) for %s", fresh.Email)
	}
	if _, ok := errs[disabledButFresh.Email]; ok {
		t.Errorf("disabled account %s must be skipped entirely, not attempted", disabledButFresh.Email)
	}
	if _, ok := errs[alreadyActivated.Email]; ok {
		t.Errorf("already-activated account %s must be skipped, not attempted", alreadyActivated.Email)
	}
}
