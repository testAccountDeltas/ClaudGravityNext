package accounts

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"claudgravity/internal/config"
)

// 64 goroutines hammer selection, cooldowns, toggle and delete at once.
// Owners 0..7 each cool their own account and then must never be handed it
// again; the rest churn. Run with -race in CI.
func TestSelection_ConcurrentMixedOperations(t *testing.T) {
	freshSelector(t)
	const stable = 8
	var accs []*Record
	for i := 0; i < stable; i++ {
		accs = append(accs, &Record{Email: fmt.Sprintf("s%d@example.com", i), ProjectID: "p"})
	}
	for i := 0; i < 4; i++ {
		accs = append(accs, &Record{Email: fmt.Sprintf("churn%d@example.com", i), ProjectID: "p"})
	}
	seedAccounts(t, accs...)
	am := globalAccountManager
	sel := globalSelector
	// A ninth stable-but-never-cooled account keeps the pool non-exhausted.
	am.mu.Lock()
	am.accounts["always@example.com"] = &Record{Email: "always@example.com", ProjectID: "p"}
	am.mu.Unlock()

	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			tenant := config.TenantConfig{}
			if g%3 == 0 {
				tenant.Strategy = "round_robin"
			}
			switch {
			case g < stable:
				own := fmt.Sprintf("s%d@example.com", g)
				am.MarkAccountCooldown(own, time.Hour)
				for i := 0; i < 30; i++ {
					acc, err := sel.SelectAccountForModel(fmt.Sprint("k", g), "", tenant, nil, "m")
					if err == nil && acc.Email == own {
						errs <- "cooled account " + own + " was returned"
						return
					}
				}
			case g < 24:
				for i := 0; i < 30; i++ {
					_, _ = sel.SelectAccountForModel(fmt.Sprint("k", g), fmt.Sprint("c", g%5), tenant, nil, "m")
					_ = sel.GetOrAssignStickyAccount(fmt.Sprint("k", g), tenant)
				}
			case g < 40:
				for i := 0; i < 30; i++ {
					am.MarkModelCooldown(fmt.Sprintf("s%d@example.com", i%stable), "m", time.Duration(i+1)*time.Millisecond)
					sel.RememberAffinity(fmt.Sprint("c", g), "always@example.com")
					sel.UnstickAccount(fmt.Sprintf("s%d@example.com", i%stable))
				}
			case g < 52:
				for i := 0; i < 30; i++ {
					_ = am.ToggleAccountDisabled(fmt.Sprintf("churn%d@example.com", g%4), i%2 == 0)
					_ = sel.EarliestAvailability("m")
				}
			default:
				for i := 0; i < 10; i++ {
					e := fmt.Sprintf("churn%d@example.com", g%4)
					_ = am.DeleteAccount(e)
					am.mu.Lock()
					am.accounts[e] = &Record{Email: e, ProjectID: "p"}
					am.mu.Unlock()
					_ = am.GetAccounts()
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	// After the storm the never-cooled account must still be selectable.
	acc, err := sel.SelectAccountForModel("final", "", config.TenantConfig{}, nil, "other-model")
	if err != nil || acc == nil {
		t.Fatalf("pool wedged after storm: %v", err)
	}
}

// A model cooldown set concurrently must be visible to the very next pick by
// the goroutine that set it.
func TestSelection_ModelCooldownVisibleImmediately(t *testing.T) {
	freshSelector(t)
	pool(t, "a", "b", "c", "d")
	sel := globalSelector
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			model := fmt.Sprint("model-", g)
			own := fmt.Sprintf("%c@example.com", 'a'+rune(g%4))
			globalAccountManager.MarkModelCooldown(own, model, time.Hour)
			for i := 0; i < 20; i++ {
				acc, err := sel.SelectAccountForModel("k", "", config.TenantConfig{Strategy: "round_robin"}, nil, model)
				if err == nil && acc.Email == own {
					t.Errorf("model-cooled %s returned for %s", own, model)
					return
				}
			}
		}(g)
	}
	wg.Wait()
}
