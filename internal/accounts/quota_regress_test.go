package accounts

import (
	"testing"
	"time"

	"claudgravity/internal/config"
)

// Regression tests for the W6/W9 quota/selection fixes. Each fails if the
// corresponding fix is reverted (see the comment on each).

func gemini(bs ...QuotaBucket) []QuotaGroup {
	return []QuotaGroup{{DisplayName: "Gemini Models", Buckets: bs}}
}

// W9: a bucket at or below 1% blocks the account until THAT bucket's own
// reset - not the 5h window, not a flat 15 minutes. Reverting to the old 5%
// threshold fails the 0.04 case; reverting to "earliest/any reset" fails the
// until assertions.
func TestRegress_BucketAtOnePercentBlocksUntilItsOwnReset(t *testing.T) {
	now := time.Now()
	weeklyReset := now.Add(72 * time.Hour)
	fiveReset := now.Add(2 * time.Hour)
	cases := []struct {
		name  string
		quota []QuotaGroup
		want  time.Time // zero = usable
	}{
		{"weekly at 0.5% blocks until weekly reset", gemini(
			QuotaBucket{Window: "weekly", RemainingFraction: 0.005, ResetTime: weeklyReset.Format(time.RFC3339)},
			QuotaBucket{Window: "5h", RemainingFraction: 0.9, ResetTime: fiveReset.Format(time.RFC3339)}), weeklyReset},
		{"5h at 0 blocks only until the 5h reset", gemini(
			QuotaBucket{Window: "weekly", RemainingFraction: 0.9, ResetTime: weeklyReset.Format(time.RFC3339)},
			QuotaBucket{Window: "5h", RemainingFraction: 0, ResetTime: fiveReset.Format(time.RFC3339)}), fiveReset},
		{"exactly 1% still blocks", gemini(
			QuotaBucket{Window: "5h", RemainingFraction: 0.01, ResetTime: fiveReset.Format(time.RFC3339)}), fiveReset},
		{"4% is NOT exhausted (old 5% threshold)", gemini(
			QuotaBucket{Window: "5h", RemainingFraction: 0.04, ResetTime: fiveReset.Format(time.RFC3339)}), time.Time{}},
		{"Claude group never blocks", []QuotaGroup{{DisplayName: "Claude and GPT", Buckets: []QuotaBucket{
			{Window: "5h", RemainingFraction: 0, ResetTime: fiveReset.Format(time.RFC3339)}}}}, time.Time{}},
	}
	for _, c := range cases {
		got := quotaBlockUntil(c.quota, now)
		if c.want.IsZero() != got.IsZero() || (!c.want.IsZero() && got.Sub(c.want).Abs() > time.Second) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestRegress_BlockedAccountIsSkippedBySelection(t *testing.T) {
	accs := pool(t, "a", "b")
	accs[0].Quotas = gemini(QuotaBucket{Window: "weekly", RemainingFraction: 0.005, ResetTime: time.Now().Add(72 * time.Hour).Format(time.RFC3339)})
	sel := NewAccountSelector()
	for _, strat := range []string{"", "sticky", "round_robin"} {
		for i := 0; i < 4; i++ {
			if e := pick(t, sel, config.TenantConfig{Strategy: strat}, ""); e != "b@example.com" {
				t.Fatalf("strategy %q returned blocked account %s", strat, e)
			}
		}
	}
}

// W9: ranking uses the WORST bucket. Averaging (or taking the best) would put
// A (0.9, 0.05) ahead of B (0.5, 0.5).
func TestRegress_RankingByWorstBucket(t *testing.T) {
	accs := pool(t, "a", "b")
	accs[0].Quotas = gemini(QuotaBucket{Window: "weekly", RemainingFraction: 0.9}, QuotaBucket{Window: "5h", RemainingFraction: 0.05})
	accs[1].Quotas = gemini(QuotaBucket{Window: "weekly", RemainingFraction: 0.5}, QuotaBucket{Window: "5h", RemainingFraction: 0.5})
	if f := getEffectiveQuotaFraction(accs[0]); f != 0.05 {
		t.Fatalf("A worst bucket = %v", f)
	}
	if e := pick(t, NewAccountSelector(), config.TenantConfig{}, ""); e != "b@example.com" {
		t.Fatalf("B has the better worst bucket, got %s", e)
	}
}

// W9: an upstream 429 on (account, model) is absorbed by the per-model
// cooldown, so the account needs no entry in the request's excluded set (and
// therefore no slot of the 3-attempt budget): the next selection simply
// routes around it, other models are unaffected, and an exhausted pool is
// reported as an error instead of re-offering the 429'd account.
func TestRegress_429RoutesAroundWithoutExclusion(t *testing.T) {
	pool(t, "a", "b")
	sel := NewAccountSelector()
	rr := config.TenantConfig{Strategy: "round_robin"}
	globalAccountManager.MarkModelCooldown("a@example.com", "m", 15*time.Minute)
	for i := 0; i < 4; i++ {
		acc, err := sel.SelectAccountForModel("k", "", rr, nil, "m")
		if err != nil || acc.Email != "b@example.com" {
			t.Fatalf("429'd account re-offered without exclusion: %v %v", acc, err)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		seen[pick(t, sel, rr, "other")] = true
	}
	if !seen["a@example.com"] {
		t.Fatal("per-model cooldown must not cool the account for other models")
	}
	globalAccountManager.MarkModelCooldown("b@example.com", "m", 15*time.Minute)
	if _, err := sel.SelectAccountForModel("k", "", rr, nil, "m"); err == nil {
		t.Fatal("pool exhausted for m must error, not loop on a cooled account")
	}
}

// EarliestAvailability must be the MINIMUM over the pool (feeds Retry-After):
// a max/first-found implementation returns 30m or 20m below.
func TestRegress_EarliestAvailabilityIsPoolMinimum(t *testing.T) {
	now := time.Now()
	pool(t, "a", "b", "c", "q", "off")
	am := globalAccountManager
	am.mu.Lock()
	am.accounts["a@example.com"].CooldownUntil = now.Add(30 * time.Minute)
	am.accounts["b@example.com"].CooldownUntil = now.Add(10 * time.Minute)
	am.accounts["c@example.com"].ModelCooldowns = map[string]time.Time{"m": now.Add(20 * time.Minute)}
	am.accounts["q@example.com"].Quotas = gemini(QuotaBucket{Window: "5h", RemainingFraction: 0, ResetTime: now.Add(25 * time.Minute).Format(time.RFC3339)})
	am.accounts["off@example.com"].Disabled = true
	am.mu.Unlock()
	sel := NewAccountSelector()

	got := sel.EarliestAvailability("m")
	if d := got.Sub(now.Add(10 * time.Minute)).Abs(); d > 5*time.Second {
		t.Fatalf("want ~10m (account b), got %v from now", time.Until(got))
	}
	// c is free for another model, so the pool is available right now.
	if !sel.EarliestAvailability("other").IsZero() {
		t.Fatal("c is free for other models: zero time expected")
	}
	am.mu.Lock()
	am.accounts["b@example.com"].CooldownUntil = time.Time{}
	am.mu.Unlock()
	if !sel.EarliestAvailability("m").IsZero() {
		t.Fatal("b is free now: zero time expected")
	}
	seedAccounts(t)
	if !sel.EarliestAvailability("m").IsZero() {
		t.Fatal("empty pool: zero time expected")
	}
}
