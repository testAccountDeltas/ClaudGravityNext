// Tests for the new ephemeral.Store abstraction (Part 6.1 of the spec) —
// not a characterization test against v1 (there's no single v1 function
// this replaces one-to-one), but this is exactly the kind of foundational
// new code that most needs its own correctness tests, since five
// different call sites across the next phases will all trust it.
package ephemeral

import (
	"sync"
	"testing"
	"time"
)

func TestStore_SetThenGet(t *testing.T) {
	s := New[string](time.Hour, time.Minute)
	defer s.Close()
	s.Set("k", "v")
	got, ok := s.Get("k")
	if !ok || got != "v" {
		t.Fatalf("expected (v, true), got (%q, %v)", got, ok)
	}
}

func TestStore_GetMissingKey(t *testing.T) {
	s := New[string](time.Hour, time.Minute)
	defer s.Close()
	got, ok := s.Get("missing")
	if ok || got != "" {
		t.Fatalf("expected (\"\", false) for a missing key, got (%q, %v)", got, ok)
	}
}

func TestStore_ExpiresAfterTTL(t *testing.T) {
	s := New[int](30*time.Millisecond, time.Hour) // sweep interval irrelevant here — Get checks exp directly
	defer s.Close()
	s.Set("k", 42)
	if _, ok := s.Get("k"); !ok {
		t.Fatal("expected the value to be present immediately after Set")
	}
	time.Sleep(60 * time.Millisecond)
	if _, ok := s.Get("k"); ok {
		t.Fatal("expected the value to report absent once its TTL has passed, even before the next sweep tick")
	}
}

func TestStore_SweepActuallyRemovesExpiredEntries(t *testing.T) {
	s := New[int](20*time.Millisecond, 30*time.Millisecond)
	defer s.Close()
	s.Set("k", 1)
	if s.Len() != 1 {
		t.Fatalf("expected 1 entry right after Set, got %d", s.Len())
	}
	time.Sleep(120 * time.Millisecond) // well past both TTL and one sweep tick
	if s.Len() != 0 {
		t.Fatalf("expected the sweep goroutine to have removed the expired entry, got %d entries remaining", s.Len())
	}
}

func TestStore_SetResetsTTL(t *testing.T) {
	// Use 200ms TTL with 120ms steps to have plenty of margin over Windows
	// 15.6ms timer quantum jitter: 120ms + 120ms = 240ms > 200ms (would be
	// expired without reset), but 120ms < 200ms (still alive after reset).
	s := New[int](200*time.Millisecond, time.Hour)
	defer s.Close()
	s.Set("k", 1)
	time.Sleep(120 * time.Millisecond)
	s.Set("k", 2) // refresh before the first TTL would have expired
	time.Sleep(120 * time.Millisecond)
	got, ok := s.Get("k")
	if !ok || got != 2 {
		t.Fatalf("expected the refreshed value to still be present, got (%d, %v)", got, ok)
	}
}

func TestStore_Delete(t *testing.T) {
	s := New[int](time.Hour, time.Hour)
	defer s.Close()
	s.Set("k", 1)
	s.Delete("k")
	if _, ok := s.Get("k"); ok {
		t.Fatal("expected the key to be gone immediately after Delete")
	}
}

func TestStore_PointerValueMutationsPersist(t *testing.T) {
	// Exercises the exact pattern internal/editrepair's nested
	// sessionID -> filePath -> content map needs: V as a pointer to a
	// struct, so a Get-then-mutate round trip actually persists, unlike
	// a value-type V (which Get would return a copy of).
	type entry struct{ files map[string]string }
	s := New[*entry](time.Hour, time.Hour)
	defer s.Close()
	s.Set("session1", &entry{files: map[string]string{}})

	e, ok := s.Get("session1")
	if !ok {
		t.Fatal("expected session1 to be present")
	}
	e.files["a.go"] = "content"

	e2, _ := s.Get("session1")
	if e2.files["a.go"] != "content" {
		t.Fatal("mutating the pointed-to struct via one Get must be visible to a later Get — this is the exact pattern the nested-map ports rely on")
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	s := New[int](time.Hour, time.Hour)
	defer s.Close()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := "k"
			s.Set(key, n)
			s.Get(key)
			s.Delete(key)
		}(i)
	}
	wg.Wait() // run this test with -race to actually prove anything
}
