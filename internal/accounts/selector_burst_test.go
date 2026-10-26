package accounts

import (
	"fmt"
	"testing"

	"claudgravity/internal/config"
)

// Parallel subagents are a burst of new conversations. They share no cache, so
// they must not all land on the sticky account.
func TestSelectAccount_BurstOfNewConversationsIsSpread(t *testing.T) {
	seedAccounts(t,
		&Record{Email: "a@example.com"},
		&Record{Email: "b@example.com"},
		&Record{Email: "c@example.com"},
	)
	sel := NewAccountSelector()
	sel.stickyMap["key1"] = "a@example.com"

	counts := map[string]int{}
	for i := 0; i < 9; i++ {
		acc, err := sel.SelectAccount("key1", fmt.Sprintf("sid:session:instance%d", i), config.TenantConfig{})
		if err != nil {
			t.Fatal(err)
		}
		counts[acc.Email]++
	}
	if counts["a@example.com"] > newConvBurstLimit+1 {
		t.Fatalf("the sticky account took too many new conversations of the burst: %v", counts)
	}
	if len(counts) < 2 {
		t.Fatalf("the burst was not spread: %v", counts)
	}
}

// Requests with no conversation anchor cannot be told apart, so they keep the
// per-API-key stickiness (one account, one warm cache).
func TestSelectAccount_NoAnchorStaysOnTheStickyAccount(t *testing.T) {
	seedAccounts(t,
		&Record{Email: "a@example.com"},
		&Record{Email: "b@example.com"},
	)
	sel := NewAccountSelector()
	sel.stickyMap["key1"] = "a@example.com"
	for i := 0; i < 8; i++ {
		acc, err := sel.SelectAccount("key1", "", config.TenantConfig{})
		if err != nil || acc.Email != "a@example.com" {
			t.Fatalf("request %d left the sticky account: %v %v", i, acc, err)
		}
	}
}

// A conversation that already has an affinity keeps its account however busy
// the burst is: the cache is the point of the binding.
func TestSelectAccount_BoundConversationIgnoresTheBurstRule(t *testing.T) {
	seedAccounts(t,
		&Record{Email: "a@example.com"},
		&Record{Email: "b@example.com"},
	)
	sel := NewAccountSelector()
	sel.stickyMap["key1"] = "a@example.com"
	sel.RememberAffinity("sid:s:main-thread", "a@example.com")
	for i := 0; i < 6; i++ {
		_, _ = sel.SelectAccount("key1", fmt.Sprintf("sid:s:new%d", i), config.TenantConfig{})
	}
	acc, err := sel.SelectAccount("key1", "sid:s:main-thread", config.TenantConfig{})
	if err != nil || acc.Email != "a@example.com" {
		t.Fatalf("a bound conversation moved: %v %v", acc, err)
	}
}
