package cacheusage

import "testing"

// Regression: no cache write is reported below Gemini's real ~4096-token
// implicit-cache floor, even when the model minimum (Anthropic docs) is
// lower. Reverting the UpstreamMinTokens check makes the "just under" cases
// report a write that upstream never serves.
func TestRegress_UpstreamMinTokensFloor(t *testing.T) {
	if UpstreamMinTokens != 4096 {
		t.Fatalf("floor constant changed: %d", UpstreamMinTokens)
	}
	// models whose own minimum is below the upstream floor
	for _, model := range []string{"claude-3-5-sonnet-20241022", "claude-sonnet-4-5", "claude-haiku-3-5"} {
		if MinTokens(model) >= UpstreamMinTokens {
			t.Fatalf("%s: precondition, model min %d must be below the floor", model, MinTokens(model))
		}
		for _, tc := range []struct {
			prefix    int64
			wantWrite bool
		}{
			{UpstreamMinTokens - 1, false},
			{UpstreamMinTokens, true},
			{UpstreamMinTokens + 1, true},
			{MinTokens(model), false},
		} {
			// frac 1.0: the whole uncached prompt is the cacheable prefix
			t5, t1 := Split(model, tc.prefix, 0, 1, 0)
			if got := t5+t1 > 0; got != tc.wantWrite {
				t.Errorf("%s prefix %d: write=%v (%d/%d), want %v", model, tc.prefix, got, t5, t1, tc.wantWrite)
			}
			if tc.wantWrite && t5+t1 != tc.prefix {
				t.Errorf("%s prefix %d: reported %d", model, tc.prefix, t5+t1)
			}
		}
	}
	// the prefix is uncached*frac: a big prompt with a small cached fraction
	if t5, t1 := Split("claude-sonnet-4-5", 10000, 0, 0.4, 0); t5+t1 != 0 {
		t.Errorf("0.4 of 10000 = 4000 is under the floor, got %d/%d", t5, t1)
	}
	if t5, _ := Split("claude-sonnet-4-5", 10000, 0, 0.41, 0); t5 != 4100 {
		t.Errorf("0.41 of 10000 = 4100 must report, got %d", t5)
	}
}
