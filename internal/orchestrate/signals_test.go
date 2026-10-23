package orchestrate

import (
	"strings"
	"testing"
)

func TestHasCacheControlBreakpoint_RequiresBothSubstrings(t *testing.T) {
	if hasCacheControlBreakpoint([]byte(`{"system":"x"}`)) {
		t.Fatal("expected no breakpoint without cache_control at all")
	}
	if hasCacheControlBreakpoint([]byte(`{"cache_control":{"type":"persistent"}}`)) {
		t.Fatal("expected no breakpoint when cache_control present but not ephemeral")
	}
	if !hasCacheControlBreakpoint([]byte(`{"cache_control":{"type":"ephemeral"}}`)) {
		t.Fatal("expected a breakpoint when both substrings are present")
	}
}

func TestCacheBreakpointPrefixFraction_NotFoundReturnsZero(t *testing.T) {
	// An audit caught v1's original bug: "not found at all" used to
	// return 1.0 (the worst-case guess) instead of the honest 0.0 ("no
	// breakpoint, no simulated cache behavior at all").
	if frac := cacheBreakpointPrefixFraction([]byte(`{"no":"breakpoint here"}`)); frac != 0.0 {
		t.Fatalf("expected 0.0 for no breakpoint, got %v", frac)
	}
}

func TestCacheBreakpointPrefixFraction_AtStartIsNearZero(t *testing.T) {
	body := []byte(`"cache_control":{"type":"ephemeral"},"rest":"` + strings.Repeat("x", 1000) + `"`)
	frac := cacheBreakpointPrefixFraction(body)
	if frac < 0 || frac > 0.05 {
		t.Fatalf("expected a breakpoint at byte 0 to yield a near-zero fraction, got %v", frac)
	}
}

func TestCacheBreakpointPrefixFraction_ClampedToUnitRange(t *testing.T) {
	body := []byte(strings.Repeat("x", 1000) + `"cache_control":{"type":"ephemeral"}`)
	frac := cacheBreakpointPrefixFraction(body)
	if frac < 0 || frac > 1 {
		t.Fatalf("expected fraction clamped to [0,1], got %v", frac)
	}
}

func TestCacheCreationThreshold_HaikuIsHigher(t *testing.T) {
	if got := cacheCreationThreshold("claude-haiku-5-5"); got != 2048 {
		t.Fatalf("expected 2048 for a Haiku model, got %d", got)
	}
	if got := cacheCreationThreshold("claude-opus-5-5"); got != 1024 {
		t.Fatalf("expected 1024 for a non-Haiku model, got %d", got)
	}
}

func TestWantsSingleToolUse_TopLevelField(t *testing.T) {
	if !wantsSingleToolUse([]byte(`{"disable_parallel_tool_use":true}`)) {
		t.Fatal("expected top-level disable_parallel_tool_use to be detected")
	}
}

func TestWantsSingleToolUse_NestedInToolChoice(t *testing.T) {
	// The official Anthropic SDKs nest this inside tool_choice, not at
	// the top level.
	if !wantsSingleToolUse([]byte(`{"tool_choice":{"type":"auto","disable_parallel_tool_use":true}}`)) {
		t.Fatal("expected tool_choice.disable_parallel_tool_use to be detected")
	}
}

func TestWantsSingleToolUse_AbsentIsFalse(t *testing.T) {
	if wantsSingleToolUse([]byte(`{"tool_choice":{"type":"auto"}}`)) {
		t.Fatal("expected false when neither location sets the flag")
	}
}

func TestStripThoughtFieldsForPublicAPI_RemovesThoughtAndSignature(t *testing.T) {
	body := []byte(`{"contents":[{"role":"model","parts":[{"thought":true,"text":"reasoning","thoughtSignature":"sig1"},{"functionCall":{"name":"f"},"thoughtSignature":"sig2"}]}]}`)
	out := stripThoughtFieldsForPublicAPI(body)
	s := string(out)
	if strings.Contains(s, `"thought"`) {
		t.Fatal("expected \"thought\" key removed for the public API")
	}
	if strings.Contains(s, "sig1") || strings.Contains(s, "sig2") {
		t.Fatal("expected every thoughtSignature removed, including the one on a bare functionCall part")
	}
}

func TestStripThoughtFieldsForPublicAPI_PinsNegativeThinkingBudget(t *testing.T) {
	body := []byte(`{"contents":[],"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`)
	out := stripThoughtFieldsForPublicAPI(body)
	if strings.Contains(string(out), `"thinkingBudget":-1`) {
		t.Fatal("expected ModeAuto's -1 thinkingBudget to be pinned to a fixed positive value for the public API")
	}
}

func TestStripThoughtFieldsForPublicAPI_NoOpWithoutContents(t *testing.T) {
	body := []byte(`{"generationConfig":{}}`)
	out := stripThoughtFieldsForPublicAPI(body)
	if string(out) != string(body) {
		t.Fatal("expected a no-op when the payload has no contents field at all")
	}
}

func TestHasAssistantTurns(t *testing.T) {
	if hasAssistantTurns([]byte(`{"messages":[{"role":"user","content":"hi"}]}`)) {
		t.Fatal("expected false with no assistant turn")
	}
	if !hasAssistantTurns([]byte(`{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hey"}]}`)) {
		t.Fatal("expected true once an assistant turn is present")
	}
}
