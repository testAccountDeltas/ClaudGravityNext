package accounts

import (
	"path/filepath"
	"testing"
	"time"

	"claudgravity/internal/config"
)

func TestAffinity_SurvivesARestart(t *testing.T) {
	seedAccounts(t, &Record{Email: "a@example.com"}, &Record{Email: "b@example.com"})
	path := filepath.Join(t.TempDir(), "session_affinity.json")

	before := NewAccountSelector()
	before.RememberAffinity("sid:s1:main", "a@example.com")
	before.RememberAffinity("sid:s1:agent2", "b@example.com")
	before.RememberAffinity("sid:gone", "removed@example.com") // account left the pool
	if !before.SaveAffinity(path) {
		t.Fatal("not saved")
	}

	after := NewAccountSelector() // the restarted process
	if n := after.LoadAffinity(path); n != 2 {
		t.Fatalf("restored %d, want 2 (the removed account's binding must be skipped)", n)
	}
	for key, want := range map[string]string{"sid:s1:main": "a@example.com", "sid:s1:agent2": "b@example.com"} {
		acc, err := after.SelectAccount("key1", key, config.TenantConfig{})
		if err != nil || acc.Email != want {
			t.Fatalf("%s: got %v err=%v, want %s", key, acc, err, want)
		}
	}
	if after.GetAffinityAccount("sid:gone") != "" {
		t.Fatal("a binding to an account that left the pool came back")
	}
}

func TestAffinity_ExpiredBindingsAndBadFilesAreIgnored(t *testing.T) {
	seedAccounts(t, &Record{Email: "a@example.com"})
	dir := t.TempDir()
	path := filepath.Join(dir, "session_affinity.json")

	s := NewAccountSelector()
	s.mu.Lock()
	s.bindAffinityLocked("sid:old", "a@example.com", time.Now().Add(-2*sessionAffinityTTL)) // already expired
	s.bindAffinityLocked("sid:new", "a@example.com", time.Now())
	s.mu.Unlock()
	s.SaveAffinity(path)

	r := NewAccountSelector()
	if n := r.LoadAffinity(path); n != 1 || r.GetAffinityAccount("sid:new") == "" || r.GetAffinityAccount("sid:old") != "" {
		t.Fatalf("restored %d; old=%q new=%q", n, r.GetAffinityAccount("sid:old"), r.GetAffinityAccount("sid:new"))
	}
	if NewAccountSelector().LoadAffinity(filepath.Join(dir, "missing.json")) != 0 {
		t.Fatal("a missing file restores nothing")
	}
}
