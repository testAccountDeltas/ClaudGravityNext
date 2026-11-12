package accounts

import (
	"testing"
	"time"

	"claudgravity/internal/config"
)

func resetFirstToken(t *testing.T) *time.Time {
	t.Helper()
	now := time.Now()
	globalFirstToken.mu.Lock()
	globalFirstToken.stats = make(map[string]*firstTokenStat)
	globalFirstToken.now = func() time.Time { return now }
	globalFirstToken.mu.Unlock()
	t.Cleanup(func() {
		globalFirstToken.mu.Lock()
		globalFirstToken.stats = make(map[string]*firstTokenStat)
		globalFirstToken.now = time.Now
		globalFirstToken.mu.Unlock()
	})
	return &now
}

func TestFirstToken_SlowAndRecovers(t *testing.T) {
	resetFirstToken(t)
	RecordFirstToken("a@x", 40*time.Second)
	if !IsSlowAccount("a@x") {
		t.Fatal("a 40s first token should mark the account slow")
	}
	for i := 0; i < 6; i++ {
		RecordFirstToken("a@x", 4*time.Second)
	}
	if IsSlowAccount("a@x") {
		t.Fatalf("account should recover after fast replies (avg %v)", FirstTokenAverage("a@x"))
	}
}

func TestFirstToken_StaleSlownessExpires(t *testing.T) {
	now := resetFirstToken(t)
	RecordFirstToken("a@x", 60*time.Second)
	*now = now.Add(slowRecheckAfter + time.Second)
	if IsSlowAccount("a@x") {
		t.Fatal("old slowness data must not shun an account forever")
	}
}

func TestSelect_SlowStickyAccountIsSkippedWhenAnotherIsFast(t *testing.T) {
	resetFirstToken(t)
	seedAccounts(t, &Record{Email: "slow@x"}, &Record{Email: "fast@x"})
	RecordFirstToken("slow@x", 50*time.Second)
	sel := NewAccountSelector()
	sel.stickyMap["k"] = "slow@x"
	acc, err := sel.SelectAccount("k", "", config.TenantConfig{})
	if err != nil || acc.Email != "fast@x" {
		t.Fatalf("got %v %v, want fast@x", acc, err)
	}
}

func TestSelect_AllSlowStillServes(t *testing.T) {
	resetFirstToken(t)
	seedAccounts(t, &Record{Email: "a@x"}, &Record{Email: "b@x"})
	RecordFirstToken("a@x", 50*time.Second)
	RecordFirstToken("b@x", 60*time.Second)
	sel := NewAccountSelector()
	sel.stickyMap["k"] = "a@x"
	acc, err := sel.SelectAccount("k", "", config.TenantConfig{})
	if err != nil || acc == nil {
		t.Fatalf("a pool-wide slowdown must still pick an account: %v %v", acc, err)
	}
}

func TestSelect_SingleSlowAccountStillServes(t *testing.T) {
	resetFirstToken(t)
	seedAccounts(t, &Record{Email: "only@x"})
	RecordFirstToken("only@x", 80*time.Second)
	acc, err := NewAccountSelector().SelectAccount("k", "", config.TenantConfig{})
	if err != nil || acc.Email != "only@x" {
		t.Fatalf("got %v %v", acc, err)
	}
}
