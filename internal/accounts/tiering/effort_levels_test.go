package tiering

import "testing"

// low / medium / high are fixed budgets; only xhigh+ (and no effort at all)
// lets the model size its own thinking. This must hold for every model class
// and for both ways a client asks for thinking: Claude Code sends
// thinking {type: adaptive} together with output_config.effort.
func TestResolveTieringAndEffort_EffortLevelsAreFixedBudgetsExceptXhigh(t *testing.T) {
	type want struct {
		model  string
		budget int
	}
	cases := map[string]map[string]want{
		"opus": {
			"low":       {"gemini-3.8-flash-medium", 8192}, // flagship class never drops below medium
			"medium":    {"gemini-3.8-flash-medium", 8192},
			"high":      {"gemini-3.8-flash-high", 16384},
			"xhigh":     {"gemini-3.8-flash-tiered", -1},
			"max":       {"gemini-3.8-flash-tiered", 65536},
			"ultracode": {"gemini-3.8-flash-tiered", -1},
			"":          {"gemini-3.8-flash-tiered", -1},
		},
		"sonnet": {
			"low":    {"gemini-3.8-flash-low", 2048},
			"medium": {"gemini-3.8-flash-medium", 8192},
			"high":   {"gemini-3.8-flash-high", 16384},
			"xhigh":  {"gemini-3.8-flash-tiered", -1},
			"max":    {"gemini-3.8-flash-tiered", 65536},
			"":       {"gemini-3.8-flash-medium", 8192},
		},
	}
	thinking := map[string]map[string]interface{}{
		"adaptive": {"type": "adaptive"},
		"enabled":  {"type": "enabled"},
	}
	for fam, byEffort := range cases {
		for effort, w := range byEffort {
			for kind, th := range thinking {
				req := map[string]interface{}{"model": fam, "thinking": th}
				if effort != "" {
					req["output_config"] = map[string]interface{}{"effort": effort}
				}
				model, budget := ResolveTieringAndEffort(req, "main", "main")
				wantBudget := w.budget
				if effort == "" && kind == "adaptive" {
					wantBudget = -1 // adaptive with no effort: the model decides
				}
				if model != w.model || budget != wantBudget {
					t.Errorf("%s effort=%q thinking=%s: got (%s, %d), want (%s, %d)", fam, effort, kind, model, budget, w.model, wantBudget)
				}
			}
		}
	}
}

// A client's explicit budget_tokens still wins over the effort tier.
func TestResolveTieringAndEffort_ExplicitBudgetBeatsEffort(t *testing.T) {
	req := map[string]interface{}{
		"model":         "opus",
		"thinking":      map[string]interface{}{"type": "enabled", "budget_tokens": float64(3000)},
		"output_config": map[string]interface{}{"effort": "xhigh"},
	}
	if _, b := ResolveTieringAndEffort(req, "main", "main"); b != 3000 {
		t.Fatalf("explicit budget_tokens must win: %d", b)
	}
}
