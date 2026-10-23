// Phase 1 characterization test for the sessionkey package, extracted
// from v1's warmup.go — proves the hash-first-user-turn derivation is
// deterministic and that fallbackKey takes priority over hashing, which
// is the exact fix for the prefix-cache-busting bug described in
// GenerateStableSessionID's doc comment.
package sessionkey

import "testing"

func TestGenerateStableSessionID_DeterministicForSameInput(t *testing.T) {
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	got1 := GenerateStableSessionID(payload, 0, "")
	got2 := GenerateStableSessionID(payload, 0, "")
	if got1 != got2 {
		t.Fatalf("same payload must hash to the same session id, got %q then %q", got1, got2)
	}
}

func TestGenerateStableSessionID_FallbackKeyTakesPriority(t *testing.T) {
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	withoutFallback := GenerateStableSessionID(payload, 0, "")
	withFallback := GenerateStableSessionID(payload, 0, "explicit-session-id")
	if withoutFallback == withFallback {
		t.Fatal("a non-empty fallbackKey must change the derivation (it takes priority over hashing payload text)")
	}
	// Must also be deterministic given the same fallback key.
	again := GenerateStableSessionID(payload, 0, "explicit-session-id")
	if again != withFallback {
		t.Fatalf("same fallbackKey must hash deterministically, got %q then %q", withFallback, again)
	}
}

func TestGenerateStableSessionID_GenerationChangesHash(t *testing.T) {
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	gen0 := GenerateStableSessionID(payload, 0, "")
	gen1 := GenerateStableSessionID(payload, 1, "")
	if gen0 == gen1 {
		t.Fatal("a different generation must produce a distinct (but still deterministic) id, not collide with gen 0")
	}
	gen1Again := GenerateStableSessionID(payload, 1, "")
	if gen1 != gen1Again {
		t.Fatalf("same generation must hash deterministically, got %q then %q", gen1, gen1Again)
	}
}

func TestGenerateStableSessionID_NoTextFallsBackToRandom(t *testing.T) {
	payload := []byte(`{}`)
	got1 := GenerateStableSessionID(payload, 0, "")
	got2 := GenerateStableSessionID(payload, 0, "")
	if got1 == got2 {
		t.Fatal("with no hashable text and no fallback key, two calls must get distinct random ids, not collide")
	}
}
