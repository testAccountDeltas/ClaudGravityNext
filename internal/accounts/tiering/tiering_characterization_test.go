// Phase 2 characterization test for the ported tiering package — covers
// the key routing rules unchanged from v1.
package tiering

import "testing"

func TestResolveTieringAndEffort_OpusMapsToTieredModeAuto(t *testing.T) {
	req := map[string]interface{}{"model": "claude-opus-4-5"}
	model, budget := ResolveTieringAndEffort(req, "main", "main")
	if model != "gemini-3.8-flash-tiered" || budget != -1 {
		t.Fatalf("expected opus with no explicit effort to map to tiered ModeAuto (-1), got model=%q budget=%d", model, budget)
	}
}

func TestResolveTieringAndEffort_HaikuIsZeroBudgetByDefault(t *testing.T) {
	req := map[string]interface{}{"model": "claude-haiku-4-5"}
	model, budget := ResolveTieringAndEffort(req, "main", "main")
	if model != "gemini-2.5-flash" || budget != 0 {
		t.Fatalf("expected haiku to map to gemini-2.5-flash with budget=0, got model=%q budget=%d", model, budget)
	}
}

func TestResolveTieringAndEffort_AuxiliaryRequestClassIsCheapTier(t *testing.T) {
	req := map[string]interface{}{"model": "claude-opus-4-5"}
	model, budget := ResolveTieringAndEffort(req, "auxiliary", "main")
	if model != "gemini-3.8-flash-low" || budget != 2048 {
		t.Fatalf("expected auxiliary opus request to downgrade to the cheap low tier, got model=%q budget=%d", model, budget)
	}
}

func TestResolveTieringAndEffort_LightweightSubagentIsCheapTier(t *testing.T) {
	req := map[string]interface{}{"model": "claude-opus-4-5"}
	model, budget := ResolveTieringAndEffort(req, "subagent", "sub:Explore")
	if model != "gemini-3.8-flash-low" || budget != 2048 {
		t.Fatalf("expected a known-lightweight subagent (sub:Explore) to get the cheap tier regardless of model, got model=%q budget=%d", model, budget)
	}
}

func TestResolveTieringAndEffort_ExplicitEffortLevels(t *testing.T) {
	cases := []struct {
		effort     string
		wantModel  string
		wantBudget int
	}{
		{"low", "gemini-3.8-flash-low", 2048},
		{"medium", "gemini-3.8-flash-medium", 8192},
		{"high", "gemini-3.8-flash-high", 16384},
		{"none", "gemini-3.8-flash-medium", 0},
	}
	for _, c := range cases {
		// claude-sonnet-4-6 (unlike -4-5) is in the native-3P-preserved
		// allowlist and is deliberately left unmapped — use a model that
		// actually falls into the sonnet->gemini-3.8-flash mapping branch.
		req := map[string]interface{}{
			"model":         "claude-sonnet-4-5",
			"output_config": map[string]interface{}{"effort": c.effort},
		}
		model, budget := ResolveTieringAndEffort(req, "main", "main")
		if model != c.wantModel || budget != c.wantBudget {
			t.Errorf("effort=%q: got model=%q budget=%d, want model=%q budget=%d", c.effort, model, budget, c.wantModel, c.wantBudget)
		}
	}
}

func TestResolveTieringAndEffort_OpusNeverDegradesBelowMediumOnLow(t *testing.T) {
	req := map[string]interface{}{
		"model":         "claude-opus-4-5",
		"output_config": map[string]interface{}{"effort": "low"},
	}
	model, budget := ResolveTieringAndEffort(req, "main", "main")
	if model != "gemini-3.8-flash-medium" || budget != 8192 {
		t.Fatalf("expected opus-class model to floor at medium even on explicit low effort, got model=%q budget=%d", model, budget)
	}
}

func TestResolveTieringAndEffort_EmptyModelFallsThroughSafely(t *testing.T) {
	req := map[string]interface{}{"model": ""}
	model, _ := ResolveTieringAndEffort(req, "main", "main")
	if model != "" {
		t.Fatalf("an empty input model must stay empty (not forced into a fallback tier), got %q", model)
	}
}

func TestResolveTieringAndEffort_AnthropicThinkingObjectDisabled(t *testing.T) {
	req := map[string]interface{}{
		"model":    "claude-sonnet-4-6",
		"thinking": map[string]interface{}{"type": "disabled"},
	}
	_, budget := ResolveTieringAndEffort(req, "main", "main")
	if budget != 0 {
		t.Fatalf("a standard Anthropic thinking.type=disabled must force budget=0, got %d", budget)
	}
}

func TestResolveTieringAndEffort_AnthropicThinkingObjectDisabled_OverridesEffort(t *testing.T) {
	req := map[string]interface{}{
		"model":         "claude-sonnet-4-6",
		"output_config": map[string]interface{}{"effort": "high"},
		"thinking":      map[string]interface{}{"type": "disabled"},
	}
	_, budget := ResolveTieringAndEffort(req, "main", "main")
	if budget != 0 {
		t.Fatalf("thinking.type=disabled must override effort and force budget=0, got %d", budget)
	}
}

func TestResolveTieringAndEffort_AnthropicThinkingExplicitBudget_OverridesEffort(t *testing.T) {
	req := map[string]interface{}{
		"model":         "claude-opus-4-5",
		"output_config": map[string]interface{}{"effort": "low"},
		"thinking":      map[string]interface{}{"type": "enabled", "budget_tokens": float64(4096)},
	}
	_, budget := ResolveTieringAndEffort(req, "main", "main")
	if budget != 4096 {
		t.Fatalf("explicit thinking budget must override effort and force budget=4096, got %d", budget)
	}
}

func TestResolveTieringAndEffort_AnthropicThinkingBudgetTokensZero(t *testing.T) {
	req := map[string]interface{}{
		"model":         "claude-opus-4-5",
		"output_config": map[string]interface{}{"effort": "high"},
		"thinking":      map[string]interface{}{"type": "enabled", "budget_tokens": float64(0)},
	}
	_, budget := ResolveTieringAndEffort(req, "main", "main")
	if budget != 0 {
		t.Fatalf("budget_tokens=0 must force budget=0, got %d", budget)
	}

	req2 := map[string]interface{}{
		"model":         "claude-opus-4-5",
		"output_config": map[string]interface{}{"effort": "high"},
		"thinking":      map[string]interface{}{"budget_tokens": float64(0)},
	}
	_, budget2 := ResolveTieringAndEffort(req2, "main", "main")
	if budget2 != 0 {
		t.Fatalf("budget_tokens=0 without type must force budget=0, got %d", budget2)
	}
}

func TestResolveTieringAndEffort_BareModelAlias(t *testing.T) {
	req := map[string]interface{}{"model": "opus"}
	model, _ := ResolveTieringAndEffort(req, "main", "main")
	if model != "gemini-3.8-flash-tiered" {
		t.Fatalf("the bare alias 'opus' (no claude- prefix) must map the same as claude-opus-*, got %q", model)
	}
}

func TestResolveTieringAndEffort_OpusMaxEffort(t *testing.T) {
	for _, effort := range []string{"max", "xhigh", "ultracode"} {
		req := map[string]interface{}{
			"model":         "claude-opus-4-5",
			"output_config": map[string]interface{}{"effort": effort},
		}
		model, budget := ResolveTieringAndEffort(req, "main", "main")
		if model != "gemini-3.8-flash-tiered" || budget != -1 {
			t.Fatalf("expected opus with %q effort to map to gemini-3.8-flash-tiered (-1), got model=%q budget=%d", effort, model, budget)
		}
	}
}
