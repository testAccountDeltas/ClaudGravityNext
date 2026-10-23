// Rule 9 — EphemeralTTLBounded. NEW rule, built from Part 7.6 #9's own
// description: "ни одна запись в любом ephemeral.Store не должна жить
// дольше заявленного TTL после последнего обращения (регрессионный тест
// на саму абстракцию из 6.1, не на трафик)."
//
// Like rule 8, this ignores the records argument entirely — it's a
// regression test on internal/ephemeral.Store[V]'s own TTL/sweep
// behavior (Part 6.1), run directly against a throwaway Store instance
// speccheck creates for the check, not against anything captured in
// request_captures/*.ndjson. internal/ephemeral is itself a NEW
// abstraction (not a port of a v1 file — see that package's doc
// comment) that replaced five independent, ad-hoc TTL-map copies in v1;
// this rule exists so a future change to Store's sweep/expiry logic
// (which every one of the five real callers — internal/editrepair,
// internal/loopguard, internal/stream's caches.go — now depends on
// transitively) gets caught by `go run ./cmd/speccheck`, the same CI
// gate as every other invariant, not only by internal/ephemeral's own
// package-local unit tests.
package rules

import (
	"fmt"
	"time"

	"claudgravity/internal/ephemeral"
)

func checkEphemeralTTLBounded() []Violation {
	const ttl = 30 * time.Millisecond
	const sweepEvery = 10 * time.Millisecond
	s := ephemeral.New[string](ttl, sweepEvery)
	defer s.Close()

	s.Set("k", "v")
	if _, ok := s.Get("k"); !ok {
		return []Violation{{RID: "ephemeral", Detail: "ephemeral.Store: value disappeared immediately after Set, before its TTL elapsed at all"}}
	}

	// Wait well past TTL + at least one sweep tick, with margin for
	// scheduler jitter on a loaded CI box.
	time.Sleep(ttl + 4*sweepEvery)

	if v, ok := s.Get("k"); ok {
		return []Violation{{RID: "ephemeral", Detail: fmt.Sprintf("ephemeral.Store: value %q still present %v after its %v TTL elapsed — entries must not outlive their TTL", v, ttl+4*sweepEvery, ttl)}}
	}
	return nil
}

var EphemeralTTLBounded = Rule{
	Name: "EphemeralTTLBounded",
	Check: func(records []CaptureRecord) []Violation {
		return checkEphemeralTTLBounded()
	},
}
