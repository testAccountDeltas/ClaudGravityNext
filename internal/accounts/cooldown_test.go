package accounts

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"claudgravity/internal/config"
)

func geminiGroup(buckets ...QuotaBucket) []QuotaGroup {
	return []QuotaGroup{{DisplayName: "Gemini Models", Buckets: buckets}}
}

func TestQuotaBlockUntil_ZeroBucketPlusHealthyBucket_BlocksUntilZeroReset(t *testing.T) {
	now := time.Now()
	r7 := now.Add(7 * time.Hour).Truncate(time.Second)
	g := geminiGroup(
		QuotaBucket{RemainingFraction: 0, ResetTime: r7.Format(time.RFC3339)},
		QuotaBucket{RemainingFraction: 1.0, ResetTime: now.Add(5 * time.Hour).Format(time.RFC3339)},
	)
	if got := quotaBlockUntil(g, now); !got.Equal(r7) {
		t.Fatalf("want blocked until %v, got %v", r7, got)
	}
}

func TestQuotaBlockUntil_ResetPassedBucketNotBlocking(t *testing.T) {
	now := time.Now()
	g := geminiGroup(QuotaBucket{RemainingFraction: 0, ResetTime: now.Add(-time.Minute).Format(time.RFC3339)})
	if !quotaBlockUntil(g, now).IsZero() {
		t.Fatal("bucket past its reset must count as restored")
	}
}

func TestQuotaBlockUntil_TwoExhausted_BlocksUntilLatestReset(t *testing.T) {
	now := time.Now()
	r1, r2 := now.Add(20*time.Hour).Truncate(time.Second), now.Add(70*time.Hour).Truncate(time.Second)
	g := geminiGroup(
		QuotaBucket{RemainingFraction: 0, ResetTime: r1.Format(time.RFC3339)},
		QuotaBucket{RemainingFraction: 0, ResetTime: r2.Format(time.RFC3339)},
	)
	if got := quotaBlockUntil(g, now); !got.Equal(r2) {
		t.Fatalf("want latest reset %v, got %v", r2, got)
	}
}

func TestEffectiveQuotaFraction_RanksByWorstBucket(t *testing.T) {
	acc := &Record{Quotas: geminiGroup(
		QuotaBucket{RemainingFraction: 0.0, ResetTime: time.Now().Add(7 * time.Hour).Format(time.RFC3339)},
		QuotaBucket{RemainingFraction: 1.0},
	)}
	if f := getEffectiveQuotaFraction(acc); f != 0.0 {
		t.Fatalf("want worst bucket 0, got %v", f)
	}
}

func fetchQuotaStub(t *testing.T, body string) {
	t.Helper()
	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	}))
}

func TestFetchQuota_ZeroBucketPlusHealthy_CoolsUntilReset(t *testing.T) {
	const email = "w6-weekly@example.com"
	acc := &Record{Email: email, AccessToken: "t", ExpiresAt: time.Now().Add(time.Hour), ProjectID: fallbackProjectID}
	seedAccounts(t, acc)
	fetchQuotaStub(t, fmt.Sprintf(`{"groups":[{"displayName":"Gemini Models","buckets":[
		{"bucketId":"w","remainingFraction":0.0,"resetTime":%q},
		{"bucketId":"h","remainingFraction":1.0,"resetTime":%q}]}]}`,
		time.Now().Add(7*time.Hour).Format(time.RFC3339), time.Now().Add(5*time.Hour).Format(time.RFC3339)))
	if err := globalAccountManager.FetchQuota(email); err != nil {
		t.Fatal(err)
	}
	if d := time.Until(acc.CooldownUntil); d < 6*time.Hour+50*time.Minute || d > 7*time.Hour+time.Minute {
		t.Fatalf("want cooldown ~7h, got %v", d)
	}
}

func TestFetchQuota_DoesNotClear429Cooldown(t *testing.T) {
	const email = "w6-keep@example.com"
	acc := &Record{Email: email, AccessToken: "t", ExpiresAt: time.Now().Add(time.Hour), ProjectID: fallbackProjectID}
	seedAccounts(t, acc)
	globalAccountManager.MarkAccountCooldown(email, 10*time.Minute)
	fetchQuotaStub(t, `{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"h","remainingFraction":0.9}]}]}`)
	if err := globalAccountManager.FetchQuota(email); err != nil {
		t.Fatal(err)
	}
	if time.Until(acc.CooldownUntil) < 9*time.Minute {
		t.Fatalf("green quota poll cancelled an error cooldown: %v", acc.CooldownUntil)
	}
}

func TestSelectAccountForModel_ModelCooldownIsPerModel(t *testing.T) {
	seedAccounts(t, &Record{Email: "a@example.com"}, &Record{Email: "b@example.com"})
	globalAccountManager.MarkModelCooldown("a@example.com", "gemini-3.8-flash-tiered", 10*time.Minute)
	sel := NewAccountSelector()
	cfg := config.TenantConfig{StickyAccount: "a@example.com"}

	acc, err := sel.SelectAccountForModel("k", "", cfg, nil, "gemini-3.8-flash-tiered")
	if err != nil || acc.Email != "b@example.com" {
		t.Fatalf("cooled model must be routed to b, got %v err=%v", acc, err)
	}
	acc, err = sel.SelectAccountForModel("k", "", cfg, nil, "gemini-3.8-flash-low")
	if err != nil || acc.Email != "a@example.com" {
		t.Fatalf("other model must still use a, got %v err=%v", acc, err)
	}
}

func TestFetchQuota_KeepsModelCooldown(t *testing.T) {
	const email = "w6-model@example.com"
	acc := &Record{Email: email, AccessToken: "t", ExpiresAt: time.Now().Add(time.Hour), ProjectID: fallbackProjectID}
	seedAccounts(t, acc)
	globalAccountManager.MarkModelCooldown(email, "m1", 5*time.Minute)
	fetchQuotaStub(t, `{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"h","remainingFraction":0.9}]}]}`)
	_ = globalAccountManager.FetchQuota(email)
	if modelCooldownUntil(acc, "m1", time.Now()).IsZero() {
		t.Fatal("quota refresh wiped the per-model cooldown")
	}
}

func TestEarliestAvailability(t *testing.T) {
	now := time.Now()
	seedAccounts(t,
		&Record{Email: "a@example.com", CooldownUntil: now.Add(20 * time.Minute)},
		&Record{Email: "b@example.com", ModelCooldowns: map[string]time.Time{"m": now.Add(5 * time.Minute)}},
		&Record{Email: "off@example.com", Disabled: true},
	)
	sel := NewAccountSelector()
	got := sel.EarliestAvailability("m")
	if d := time.Until(got); d < 4*time.Minute || d > 6*time.Minute {
		t.Fatalf("want ~5m (model m on b), got %v", d)
	}
	if !sel.EarliestAvailability("other").IsZero() {
		t.Fatal("b is free for other models: want zero time")
	}
}

func TestSticky_SpreadsNewSessionsOffOverloadedAccount(t *testing.T) {
	seedAccounts(t, &Record{Email: "a@example.com"}, &Record{Email: "b@example.com"}, &Record{Email: "c@example.com"})
	sel := NewAccountSelector()
	cfg := config.TenantConfig{Strategy: "sticky"}
	first, _ := sel.SelectAccount("k", "", cfg)
	counts := map[string]int{}
	for i := 0; i < 120; i++ {
		acc, err := sel.SelectAccount("k", "", cfg)
		if err != nil {
			t.Fatal(err)
		}
		counts[acc.Email]++
	}
	max := 0
	for _, n := range counts {
		if n > max {
			max = n
		}
	}
	if len(counts) < 2 || max > 90 {
		t.Fatalf("sticky skew unbounded: first=%s counts=%v", first.Email, counts)
	}
}

func TestSticky_ExistingAffinityNotMoved(t *testing.T) {
	seedAccounts(t, &Record{Email: "a@example.com"}, &Record{Email: "b@example.com"})
	sel := NewAccountSelector()
	cfg := config.TenantConfig{Strategy: "sticky"}
	sel.RememberAffinity("conv-1", "a@example.com")
	for i := 0; i < 40; i++ {
		acc, err := sel.SelectAccount("k", "conv-1", cfg)
		if err != nil || acc.Email != "a@example.com" {
			t.Fatalf("ongoing conversation must keep its account, got %v err=%v", acc, err)
		}
	}
}

func TestSticky_QuotaLagMovesNewSession(t *testing.T) {
	seedAccounts(t,
		&Record{Email: "a@example.com", Quotas: geminiGroup(QuotaBucket{RemainingFraction: 0.2})},
		&Record{Email: "b@example.com", Quotas: geminiGroup(QuotaBucket{RemainingFraction: 0.9})},
	)
	sel := NewAccountSelector()
	sel.setStickyLocked("k", "a@example.com")
	acc, err := sel.SelectAccount("k", "", config.TenantConfig{Strategy: "sticky"})
	if err != nil || acc.Email != "b@example.com" {
		t.Fatalf("want move to healthier b, got %v err=%v", acc, err)
	}
}
