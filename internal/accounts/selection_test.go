package accounts

import (
	"strings"
	"testing"
	"time"

	"claudgravity/internal/config"
)

func pool(t *testing.T, names ...string) []*Record {
	t.Helper()
	var accs []*Record
	for _, n := range names {
		accs = append(accs, &Record{Email: n + "@example.com", ProjectID: "p"})
	}
	seedAccounts(t, accs...)
	return accs
}

func pick(t *testing.T, sel *AccountSelector, tenant config.TenantConfig, model string) string {
	t.Helper()
	acc, err := sel.SelectAccountForModel("key", "", tenant, nil, model)
	if err != nil {
		t.Fatal(err)
	}
	return acc.Email
}

func TestRoundRobin_SixPicksThreeAccountsTwoEach_StartsAtFirst(t *testing.T) {
	pool(t, "a", "b", "c")
	sel := NewAccountSelector()
	rr := config.TenantConfig{Strategy: "round_robin"}
	var order []string
	counts := map[string]int{}
	for i := 0; i < 6; i++ {
		e := pick(t, sel, rr, "")
		order = append(order, e)
		counts[e]++
	}
	for e, n := range counts {
		if n != 2 {
			t.Errorf("%s picked %d times, want 2 (order %v)", e, n, order)
		}
	}
	if order[0] != "a@example.com" || order[1] != "b@example.com" || order[2] != "c@example.com" {
		t.Fatalf("first cycle must start at index 0 (old code bumped the index before use and skipped it): %v", order)
	}
}

func TestRoundRobin_SkipsCoolingAccount(t *testing.T) {
	accs := pool(t, "a", "b", "c")
	accs[1].CooldownUntil = time.Now().Add(time.Hour)
	sel := NewAccountSelector()
	rr := config.TenantConfig{Strategy: "round_robin"}
	for i := 0; i < 8; i++ {
		if e := pick(t, sel, rr, ""); e == "b@example.com" {
			t.Fatal("cooling account returned by round robin")
		}
	}
}

func TestModelCooldown_KeyNormalizationAndAccountWideFallback(t *testing.T) {
	accs := pool(t, "a")
	am := globalAccountManager
	am.MarkModelCooldown("a@example.com", "  Gemini-3-PRO ", time.Hour)
	if _, ok := accs[0].ModelCooldowns["gemini-3-pro"]; !ok || len(accs[0].ModelCooldowns) != 1 {
		t.Fatalf("key not normalized: %v", accs[0].ModelCooldowns)
	}
	if availableFor(accs[0], "GEMINI-3-pro") {
		t.Fatal("differently-cased name must hit the same cooldown")
	}
	if !availableFor(accs[0], "gemini-flash") {
		t.Fatal("other models stay available")
	}
	if !accs[0].CooldownUntil.IsZero() {
		t.Fatal("per-model cooldown must not cool the account")
	}

	am.MarkModelCooldown("a@example.com", "   ", time.Hour) // empty -> account-wide
	if accs[0].CooldownUntil.IsZero() || availableFor(accs[0], "gemini-flash") {
		t.Fatal("empty model must cool the whole account")
	}
}

func TestModelCooldown_ShorterNeverShortensLonger(t *testing.T) {
	accs := pool(t, "a")
	am := globalAccountManager
	am.MarkModelCooldown("a@example.com", "m", time.Hour)
	long := accs[0].ModelCooldowns["m"]
	am.MarkModelCooldown("a@example.com", "m", time.Second)
	if !accs[0].ModelCooldowns["m"].Equal(long) {
		t.Fatal("shorter model cooldown shortened a longer one")
	}
	am.MarkAccountCooldown("a@example.com", time.Hour)
	longAcc := accs[0].CooldownUntil
	am.MarkAccountCooldown("a@example.com", time.Second)
	if !accs[0].CooldownUntil.Equal(longAcc) || !accs[0].hardCooldownUntil.Equal(longAcc) {
		t.Fatal("shorter account cooldown shortened a longer one")
	}
}

func TestModelCooldown_Expiry(t *testing.T) {
	accs := pool(t, "a")
	globalAccountManager.MarkModelCooldown("a@example.com", "m", time.Hour)
	until := accs[0].ModelCooldowns["m"]
	if modelCooldownUntil(accs[0], "m", until.Add(-time.Second)).IsZero() {
		t.Fatal("must still cool just before expiry")
	}
	if !modelCooldownUntil(accs[0], "m", until.Add(time.Nanosecond)).IsZero() {
		t.Fatal("must be free right after expiry")
	}
	// Selection level: an already-elapsed per-model cooldown no longer blocks.
	accs[0].ModelCooldowns["m"] = time.Now().Add(-time.Millisecond)
	if !availableFor(accs[0], "m") {
		t.Fatal("elapsed cooldown still blocking selection")
	}
	// Unknown account: no panic, no effect.
	globalAccountManager.MarkModelCooldown("ghost@example.com", "m", time.Hour)
	globalAccountManager.MarkAccountCooldown("ghost@example.com", time.Hour)
}

func TestGetOrAssignStickyAccount_NeverPinsToCoolingAccount(t *testing.T) {
	accs := pool(t, "a", "b")
	accs[0].CooldownUntil = time.Now().Add(time.Hour)
	sel := NewAccountSelector()
	got := sel.GetOrAssignStickyAccount("k", config.TenantConfig{})
	if got == nil || got.Email != "b@example.com" {
		t.Fatalf("got %+v", got)
	}
	if sel.stickyMap["k"] != "b@example.com" {
		t.Fatalf("pinned to %q", sel.stickyMap["k"])
	}
}

func TestGetOrAssignStickyAccount_RepinsWhenAssignedAccountCools(t *testing.T) {
	accs := pool(t, "a", "b")
	sel := NewAccountSelector()
	first := sel.GetOrAssignStickyAccount("k", config.TenantConfig{})
	again := sel.GetOrAssignStickyAccount("k", config.TenantConfig{})
	if first.Email != again.Email {
		t.Fatalf("assignment must be stable: %s vs %s", first.Email, again.Email)
	}
	for _, a := range accs {
		if a.Email == first.Email {
			a.CooldownUntil = time.Now().Add(time.Hour)
		}
	}
	moved := sel.GetOrAssignStickyAccount("k", config.TenantConfig{})
	if moved.Email == first.Email || sel.stickyMap["k"] != moved.Email {
		t.Fatalf("still on cooling %s (moved=%s pinned=%s)", first.Email, moved.Email, sel.stickyMap["k"])
	}
}

func TestGetOrAssignStickyAccount_PinnedTenantAccountRules(t *testing.T) {
	accs := pool(t, "a", "b")
	sel := NewAccountSelector()
	tenant := config.TenantConfig{StickyAccount: "a@example.com"}
	if got := sel.GetOrAssignStickyAccount("k", tenant); got.Email != "a@example.com" {
		t.Fatalf("pinned healthy account must win, got %s", got.Email)
	}
	accs[0].Disabled = true
	if got := sel.GetOrAssignStickyAccount("k", tenant); got.Email != "b@example.com" {
		t.Fatalf("disabled pinned account must be skipped, got %s", got.Email)
	}
	accs[0].Disabled = false
	accs[0].CooldownUntil = time.Now().Add(time.Hour)
	if got := sel.GetOrAssignStickyAccount("k2", tenant); got.Email != "b@example.com" {
		t.Fatalf("cooling pinned account must be skipped, got %s", got.Email)
	}
}

func TestGetOrAssignStickyAccount_EdgeCases(t *testing.T) {
	seedAccounts(t)
	sel := NewAccountSelector()
	if sel.GetOrAssignStickyAccount("k", config.TenantConfig{}) != nil {
		t.Fatal("empty pool must yield nil")
	}
	accs := pool(t, "a", "b")
	accs[0].CooldownUntil = time.Now().Add(time.Hour)
	accs[1].CooldownUntil = time.Now().Add(time.Hour)
	if sel.GetOrAssignStickyAccount("k", config.TenantConfig{}) == nil {
		t.Fatal("all cooling: documented last resort is a non-nil account")
	}
	accs[0].Disabled, accs[1].Disabled = true, true
	if sel.GetOrAssignStickyAccount("", config.TenantConfig{}) == nil {
		t.Fatal("must never return nil for a non-empty pool")
	}
}

func TestSelect_PinnedDisabledOrCoolingTenantAccountFallsThrough(t *testing.T) {
	accs := pool(t, "a", "b")
	sel := NewAccountSelector()
	tenant := config.TenantConfig{StickyAccount: "a@example.com"}
	accs[0].Disabled = true
	if e := pick(t, sel, tenant, ""); e != "b@example.com" {
		t.Fatalf("got %s", e)
	}
	accs[0].Disabled = false
	globalAccountManager.MarkModelCooldown("a@example.com", "m", time.Hour)
	if e := pick(t, sel, tenant, "m"); e != "b@example.com" {
		t.Fatalf("model-cooled pinned account must be skipped, got %s", e)
	}
	if e := pick(t, sel, tenant, "other"); e != "a@example.com" {
		t.Fatalf("pin still valid for other models, got %s", e)
	}
}

func TestAffinity_TTLExpiryAndRefresh(t *testing.T) {
	pool(t, "a", "b")
	sel := NewAccountSelector()
	sel.RememberAffinity("conv", "b@example.com")
	if sel.GetAffinityAccount("conv") != "b@example.com" {
		t.Fatal("binding missing")
	}
	acc, _ := sel.SelectAccountForModel("", "conv", config.TenantConfig{}, nil, "")
	if acc.Email != "b@example.com" {
		t.Fatalf("affinity must win, got %s", acc.Email)
	}
	entry := sel.affinityIdx["conv"].Value.(*affinityLRUEntry)
	if rem := time.Until(entry.expiresAt); rem < sessionAffinityTTL-time.Minute {
		t.Fatalf("hit must refresh the lease, remaining %v", rem)
	}
	entry.expiresAt = time.Now().Add(-time.Second) // elapsed
	if sel.GetAffinityAccount("conv") != "" {
		t.Fatal("expired binding must read as empty")
	}
	acc, _ = sel.SelectAccountForModel("", "conv", config.TenantConfig{}, nil, "")
	if _, still := sel.affinityIdx["conv"]; still {
		t.Fatal("selection must drop the expired binding")
	}
	if acc == nil {
		t.Fatal("expected a fallback pick")
	}
	sel.RememberAffinity("", "a@example.com")
	sel.RememberAffinity("x", "")
	if len(sel.affinityIdx) != 0 {
		t.Fatal("empty key/email must not bind")
	}
}

func TestAffinity_CoolingBoundAccountIsNotReturned(t *testing.T) {
	accs := pool(t, "a", "b")
	sel := NewAccountSelector()
	sel.RememberAffinity("conv", "a@example.com")
	accs[0].CooldownUntil = time.Now().Add(time.Hour)
	for i := 0; i < 5; i++ {
		acc, err := sel.SelectAccountForModel("", "conv", config.TenantConfig{}, nil, "")
		if err != nil || acc.Email != "b@example.com" {
			t.Fatalf("got %v %v", acc, err)
		}
	}
}

func TestSelect_ErrorWhenPoolEmptyOrExhausted(t *testing.T) {
	seedAccounts(t)
	sel := NewAccountSelector()
	if _, err := sel.SelectAccount("k", "", config.TenantConfig{}); err == nil || !strings.Contains(err.Error(), "no accounts") {
		t.Fatalf("got %v", err)
	}
	accs := pool(t, "a")
	accs[0].Disabled = true
	if _, err := sel.SelectAccount("k", "", config.TenantConfig{}); err == nil {
		t.Fatal("all disabled must error")
	}
}
