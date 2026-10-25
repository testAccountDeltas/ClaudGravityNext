package tiering

import "testing"

func covReq(model, effort string, thinking map[string]interface{}) map[string]interface{} {
	m := map[string]interface{}{"model": model}
	if effort != "" {
		m["output_config"] = map[string]interface{}{"effort": effort}
	}
	if thinking != nil {
		m["thinking"] = thinking
	}
	return m
}

func covEnabled(bt interface{}) map[string]interface{} {
	th := map[string]interface{}{"type": "enabled"}
	if bt != nil {
		th["budget_tokens"] = bt
	}
	return th
}

func TestCov_ResolveTiering(t *testing.T) {
	type tc struct {
		name, model, effort, class, agent string
		thinking                          map[string]interface{}
		wantModel                         string
		wantBudget                        int
	}
	cases := []tc{
		{"opus no effort adaptive", "claude-opus-4-5", "", "", "", map[string]interface{}{"type": "adaptive"}, "gemini-3.8-flash-tiered", -1},
		{"opus low floors to medium", "claude-opus-4-5", "low", "", "", covEnabled(nil), "gemini-3.8-flash-medium", 8192},
		{"opus off", "claude-opus-4-5", "off", "", "", covEnabled(nil), "gemini-3.8-flash-medium", 0},
		{"sonnet high", "claude-sonnet-4-5", "high", "", "", covEnabled(nil), "gemini-3.8-flash-high", 16384},
		{"sonnet medium", "claude-sonnet-4-5", "medium", "", "", covEnabled(nil), "gemini-3.8-flash-medium", 8192},
		{"sonnet low", "claude-sonnet-4-5", "low", "", "", covEnabled(nil), "gemini-3.8-flash-low", 2048},
		{"sonnet max", "sonnet", "max", "", "", covEnabled(nil), "gemini-3.8-flash-tiered", -1},
		{"sonnet default", "sonnet", "", "", "", covEnabled(nil), "gemini-3.8-flash-medium", 8192},
		{"sonnet explicit budget", "sonnet", "", "", "", covEnabled(1234), "gemini-3.8-flash-medium", 1234},
		{"sonnet explicit int64 budget", "sonnet", "", "", "", covEnabled(int64(500)), "gemini-3.8-flash-medium", 500},
		{"sonnet float budget zero", "sonnet", "", "", "", covEnabled(float64(0)), "gemini-3.8-flash-medium", 0},
		{"no thinking field => 0", "opus", "high", "", "", nil, "gemini-3.8-flash-tiered", 0},
		{"thinking disabled", "opus", "high", "", "", map[string]interface{}{"type": "disabled"}, "gemini-3.8-flash-tiered", 0},
		{"legacy bare budget", "sonnet", "", "", "", map[string]interface{}{"budget_tokens": 700}, "gemini-3.8-flash-medium", 700},
		{"aux capped", "opus", "max", "auxiliary", "", covEnabled(30000), "gemini-3.8-flash-low", 2048},
		{"aux adaptive stays cheap", "opus", "", "auxiliary", "", map[string]interface{}{"type": "adaptive"}, "gemini-3.8-flash-low", 2048},
		{"explore subagent", "opus", "max", "subagent", "sub:Explore", covEnabled(nil), "gemini-3.8-flash-low", 2048},
		{"compaction capped", "opus", "high", "compaction", "", covEnabled(50000), "gemini-3.8-flash-medium", 8192},
		{"workflow low effort", "opus", "low", "workflow", "", covEnabled(nil), "gemini-3.8-flash-medium", 8192},
		{"haiku no thinking budget", "haiku", "medium", "", "", covEnabled(nil), "gemini-2.5-flash", 0},
		{"haiku max honored", "haiku", "max", "", "", covEnabled(nil), "gemini-2.5-flash", -1},
		{"haiku adaptive keeps 0", "haiku", "", "", "", map[string]interface{}{"type": "adaptive"}, "gemini-2.5-flash", 0},
		{"native sonnet-4-6 default", "claude-sonnet-4-6", "", "", "", covEnabled(nil), "claude-sonnet-4-6", 8192},
		{"native sonnet-4-6 low", "claude-sonnet-4-6", "low", "", "", covEnabled(nil), "claude-sonnet-4-6", 2048},
		{"native sonnet-4-6 medium", "claude-sonnet-4-6", "medium", "", "", covEnabled(nil), "claude-sonnet-4-6", 8192},
		{"native sonnet-4-6 high", "claude-sonnet-4-6", "high", "", "", covEnabled(nil), "claude-sonnet-4-6", 16384},
		{"native sonnet-4-6 max", "claude-sonnet-4-6", "max", "", "", covEnabled(nil), "claude-sonnet-4-6", -1},
		{"native sonnet-4-6 none", "claude-sonnet-4-6", "none", "", "", covEnabled(nil), "claude-sonnet-4-6", 0},
		{"native sonnet-4-6 aux", "claude-sonnet-4-6", "", "auxiliary", "", covEnabled(nil), "claude-sonnet-4-6", 2048},
		{"opus thinking native", "claude-opus-4-6-thinking", "", "", "", covEnabled(nil), "claude-opus-4-6-thinking", -1},
		{"gpt-oss default", "gpt-oss-120b-medium", "", "", "", covEnabled(nil), "gpt-oss-120b-medium", 8192},
		{"legacy fallback", "claude-2.1", "", "", "", covEnabled(nil), "gemini-3.8-flash-tiered", -1},
		{"empty model preserved", "", "", "", "", covEnabled(nil), "", 8192},
		{"gemini passthrough", "gemini-3-pro", "", "", "", covEnabled(nil), "gemini-3-pro", 8192},
		{"gemini -high suffix", "gemini-x-high", "", "", "", covEnabled(nil), "gemini-x-high", 16384},
		{"gemini -low suffix", "gemini-x-low", "", "", "", covEnabled(nil), "gemini-x-low", 2048},
		{"gemini -medium suffix", "gemini-x-medium", "", "", "", covEnabled(nil), "gemini-x-medium", 8192},
		{"other -tiered low", "gemini-9-tiered", "low", "", "", covEnabled(nil), "gemini-9-low", 2048},
		{"other -tiered medium", "gemini-9-tiered", "medium", "", "", covEnabled(nil), "gemini-9-medium", 8192},
		{"other -tiered high", "gemini-9-tiered", "high", "", "", covEnabled(nil), "gemini-9-high", 16384},
		{"other -tiered max", "gemini-9-tiered", "max", "", "", covEnabled(nil), "gemini-9-tiered", -1},
		{"other -tiered disabled", "gemini-9-tiered", "disabled", "", "", covEnabled(nil), "gemini-9-medium", 0},
		{"other -tiered default", "gemini-9-tiered", "", "", "", covEnabled(nil), "gemini-9-medium", 8192},
		{"other -tiered aux", "gemini-9-tiered", "", "auxiliary", "", covEnabled(nil), "gemini-9-low", 2048},
		{"other -tiered mid", "gemini-9-tiered", "high", "workflow", "", covEnabled(nil), "gemini-9-medium", 8192},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := covReq(c.model, c.effort, c.thinking)
			m, b := ResolveTieringAndEffort(req, c.class, c.agent)
			if m != c.wantModel || b != c.wantBudget {
				t.Fatalf("got (%q,%d) want (%q,%d)", m, b, c.wantModel, c.wantBudget)
			}
			if req["model"] != c.wantModel {
				t.Fatalf("request model not rewritten: %v", req["model"])
			}
		})
	}
}

func TestCov_ResolveTiering_MessageLevelEffort(t *testing.T) {
	req := map[string]interface{}{
		"model":    "sonnet",
		"thinking": covEnabled(nil),
		"messages": []interface{}{
			map[string]interface{}{"output_config": map[string]interface{}{"effort": "low"}},
			map[string]interface{}{"role": "user"},
			map[string]interface{}{"output_config": map[string]interface{}{"effort": "HIGH"}},
		},
	}
	m, b := ResolveTieringAndEffort(req, "", "")
	if m != "gemini-3.8-flash-high" || b != 16384 {
		t.Fatalf("latest message effort must win: %s %d", m, b)
	}
	// Top-level effort beats message-level.
	req2 := covReq("sonnet", "low", covEnabled(nil))
	req2["messages"] = req["messages"]
	if m, _ := ResolveTieringAndEffort(req2, "", ""); m != "gemini-3.8-flash-low" {
		t.Fatalf("top-level must win: %s", m)
	}
}
