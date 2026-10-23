// Package ephemeral is a NEW abstraction (not a port of any single v1
// file) called for directly by
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 6.1: v1
// independently reinvented the same "map + sync.Mutex + its own
// sweepStale* goroutine" pattern FIVE times, in three different files
// (server.go's sessionBumps, sse.go's toolSignatures and
// sessionCacheFractions, edit_repair.go's sessionCWDMap and
// sessionSnapMap) — each with its own TTL, its own sweep interval, its
// own lock. Changing the expiry policy meant finding and fixing five
// places by hand, and Round 15's audits already confused which copy was
// responsible for what. One generic, tested TTL-keyed store replaces all
// five; internal/editrepair and internal/loopguard (the next two files in
// this migration phase) are its first real callers.
//
// A per-session nested map (v1's edit_repair.go sessionSnapMap, keyed
// sessionID -> filePath -> content) still fits this generic store: V is
// simply instantiated as a pointer to a small struct holding both inner
// maps (see internal/editrepair's snapshot.go once ported) — one Store
// per session-keyed CONCEPT, not one map per nested field.
package ephemeral

import (
	"sync"
	"time"
)

// Store is a generic TTL-keyed map with its own background sweep
// goroutine. One Store per logical concept (sessions, agentKey,
// toolCallID), not one per file, is the whole point.
type Store[V any] struct {
	mu   sync.Mutex
	ttl  time.Duration
	data map[string]entry[V]
	stop chan struct{}
}

type entry[V any] struct {
	val V
	exp time.Time
}

// New creates a Store whose entries expire ttl after their last Set call,
// swept at least once every sweepEvery.
func New[V any](ttl time.Duration, sweepEvery time.Duration) *Store[V] {
	s := &Store[V]{ttl: ttl, data: make(map[string]entry[V]), stop: make(chan struct{})}
	go s.sweepLoop(sweepEvery)
	return s
}

// Set stores v under key, resetting its TTL from now.
func (s *Store[V]) Set(key string, v V) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = entry[V]{val: v, exp: time.Now().Add(s.ttl)}
}

// Get returns the value stored under key and whether it was present and
// not yet expired. An expired-but-not-yet-swept entry reports ok=false,
// exactly as if it had already been removed — callers never observe a
// stale value between its logical expiry and the next sweep tick.
func (s *Store[V]) Get(key string) (V, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.data[key]
	if !ok || time.Now().After(e.exp) {
		var zero V
		return zero, false
	}
	return e.val, true
}

// Delete removes key immediately, regardless of its TTL.
func (s *Store[V]) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
}

// Len reports the number of entries currently stored, including any not
// yet swept past their expiry — mainly useful for tests.
func (s *Store[V]) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.data)
}

func (s *Store[V]) sweepLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			now := time.Now()
			s.mu.Lock()
			for k, e := range s.data {
				if now.After(e.exp) {
					delete(s.data, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

// Close stops the background sweep goroutine. Stores in this migration
// are process-lifetime singletons (same as v1's package-level maps), so
// production code never calls this — it exists for tests that create and
// discard many Store instances and would otherwise leak one goroutine per
// test.
func (s *Store[V]) Close() { close(s.stop) }
