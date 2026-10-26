// Part of the accounts package — see affinity.go. The conversation → account
// binding lives in memory, so a gateway restart used to forget it: every
// running conversation was re-assigned (often to another account) and started
// on a cold prompt cache, and a restart in the middle of a multi-agent run cost
// the whole fleet its cache at once (62 restarts in the local logs). The table
// is therefore written to a small file and read back at start-up.
package accounts

import (
	"encoding/json"
	"os"
	"time"

	"claudgravity/internal/atomicfile"
	"claudgravity/internal/logx"
)

// affinityFileEntry is one persisted binding.
type affinityFileEntry struct {
	Key       string `json:"key"`
	Email     string `json:"email"`
	ExpiresAt int64  `json:"expires_at"` // unix seconds
}

type affinityFile struct {
	// Entries run from the least recently used to the most recently used, so
	// loading them in order rebuilds the same LRU.
	Entries []affinityFileEntry `json:"entries"`
}

// affinitySnapshotLocked copies the live (unexpired) bindings, oldest first.
func (as *AccountSelector) affinitySnapshotLocked(now time.Time) affinityFile {
	var f affinityFile
	for el := as.affinityLRU.Back(); el != nil; el = el.Prev() {
		e := el.Value.(*affinityLRUEntry)
		if now.Before(e.expiresAt) {
			f.Entries = append(f.Entries, affinityFileEntry{Key: e.key, Email: e.email, ExpiresAt: e.expiresAt.Unix()})
		}
	}
	return f
}

// SaveAffinity writes the bindings to path. It reports whether a file was
// written.
func (as *AccountSelector) SaveAffinity(path string) bool {
	as.mu.Lock()
	snap := as.affinitySnapshotLocked(time.Now())
	as.mu.Unlock()
	b, err := json.Marshal(snap)
	if err != nil {
		return false
	}
	if err := atomicfile.WriteFile(path, b); err != nil {
		logx.LogGateway("[Affinity] could not save %s: %v", path, err)
		return false
	}
	return true
}

// LoadAffinity restores bindings written by SaveAffinity. Expired ones and
// ones for accounts that are no longer in the pool are skipped. It returns the
// number restored.
func (as *AccountSelector) LoadAffinity(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var f affinityFile
	if json.Unmarshal(b, &f) != nil {
		return 0
	}
	known := map[string]bool{}
	for _, a := range globalAccountManager.GetAccounts() {
		known[a.Email] = true
	}
	now := time.Now()
	as.mu.Lock()
	defer as.mu.Unlock()
	n := 0
	for _, e := range f.Entries {
		exp := time.Unix(e.ExpiresAt, 0)
		if e.Key == "" || !known[e.Email] || !now.Before(exp) {
			continue
		}
		as.bindAffinityLocked(e.Key, e.Email, now)
		// bindAffinityLocked stamps a fresh TTL; restore the saved one.
		if el, ok := as.affinityIdx[e.Key]; ok {
			el.Value.(*affinityLRUEntry).expiresAt = exp
		}
		n++
	}
	return n
}

// StartAffinityPersistence restores the table from path and then saves it every
// interval until the process ends. Call it once at start-up, after the account
// pool has been loaded.
func (as *AccountSelector) StartAffinityPersistence(path string, interval time.Duration) {
	if n := as.LoadAffinity(path); n > 0 {
		logx.LogGateway("[Affinity] restored %d conversation → account binding(s) from %s", n, path)
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			as.SaveAffinity(path)
		}
	}()
}
