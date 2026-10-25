package ctxedit

import (
	"strings"
	"testing"
)

func covParse(edit string) (Config, error) {
	return Parse([]byte(`{"messages":[],"context_management":{"edits":[` + edit + `]}}`))
}

func TestCov_ParseThinkingVariants(t *testing.T) {
	ok := map[string]func(*ThinkingEdit) bool{
		`{"type":"clear_thinking_20251015"}`:                                            func(e *ThinkingEdit) bool { return e.KeepAll },
		`{"type":"clear_thinking_20251015","keep":null}`:                                func(e *ThinkingEdit) bool { return e.KeepAll },
		`{"type":"clear_thinking_20251015","keep":"all"}`:                               func(e *ThinkingEdit) bool { return e.KeepAll },
		`{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":2}}`: func(e *ThinkingEdit) bool { return e.KeepTurns == 2 && !e.KeepAll },
	}
	for in, check := range ok {
		c, err := covParse(in)
		if err != nil || c.Thinking == nil || !check(c.Thinking) {
			t.Errorf("%s: %+v %v", in, c.Thinking, err)
		}
	}
	bad := map[string]string{
		`{"type":"clear_thinking_20251015","keep":"some"}`:                                "keep:",
		`{"type":"clear_thinking_20251015","keep":{"type":"turns","value":1}}`:            "keep.type",
		`{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":1.5}}`: "keep.value",
		`{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":"3"}}`: "keep.value",
	}
	for in, frag := range bad {
		if _, err := covParse(in); err == nil || !strings.Contains(err.Error(), frag) {
			t.Errorf("%s: err=%v want fragment %q", in, err, frag)
		}
	}
}

func TestCov_ParseToolUsesVariants(t *testing.T) {
	c, err := covParse(`{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":5},"keep":{"type":"tool_uses","value":0},"clear_at_least":{"type":"input_tokens","value":50},"exclude_tools":["a","b"],"clear_tool_inputs":true}`)
	if err != nil {
		t.Fatal(err)
	}
	tu := c.ToolUses
	if tu.TriggerToolUses != 5 || tu.TriggerTokens != 0 || tu.Keep != 0 || tu.ClearAtLeast != 50 || !tu.ClearInputs || !tu.ExcludeTools["a"] || !tu.ExcludeTools["b"] {
		t.Fatalf("%+v", tu)
	}
	d, _ := covParse(`{"type":"clear_tool_uses_20250919","trigger":null,"keep":null}`)
	if d.ToolUses.TriggerTokens != DefaultTriggerTokens || d.ToolUses.Keep != DefaultKeepToolUses {
		t.Fatalf("defaults: %+v", d.ToolUses)
	}
	bad := []string{
		`{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":0}}`,
		`{"type":"clear_tool_uses_20250919","keep":{"type":"other","value":1}}`,
		`{"type":"clear_tool_uses_20250919","keep":{"type":"tool_uses","value":1.5}}`,
		`{"type":"clear_tool_uses_20250919","keep":{"type":"tool_uses"}}`,
		`{"type":"clear_tool_uses_20250919","clear_at_least":{"type":"tool_uses","value":5}}`,
		`{"type":"clear_tool_uses_20250919","clear_at_least":{"type":"input_tokens","value":0}}`,
		`{"type":"clear_tool_uses_20250919","exclude_tools":"x"}`,
	}
	for _, in := range bad {
		if _, err := covParse(in); err == nil {
			t.Errorf("want error: %s", in)
		}
	}
}

func TestCov_ParseStructuralErrors(t *testing.T) {
	for _, body := range []string{
		`{"context_management":"x"}`,
		`{"context_management":{"edits":{}}}`,
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("want error: %s", body)
		}
	}
	c, err := Parse([]byte(`{"context_management":{}}`))
	if err != nil || !c.Present || c.ToolUses != nil {
		t.Fatalf("empty cm: %+v %v", c, err)
	}
	// Invalid thinking edit propagates with its index.
	if _, err := covParse(`{"type":"clear_thinking_20251015","keep":"x"}`); err == nil || !strings.Contains(err.Error(), "edits.0") {
		t.Fatalf("index missing: %v", err)
	}
}

func TestCov_StripFieldAndResponse(t *testing.T) {
	out := string(StripField([]byte(`{"a":1,"context_management":{"edits":[]},"b":2}`)))
	if strings.Contains(out, "context_management") || !strings.Contains(out, `"a":1`) || !strings.Contains(out, `"b":2`) {
		t.Fatalf("StripField: %s", out)
	}
	if string(StripField([]byte(`{"a":1}`))) != `{"a":1}` {
		t.Fatal("absent field must be a no-op")
	}
	r := Response(nil)
	if ae, ok := r["applied_edits"].([]AppliedEdit); !ok || ae == nil || len(ae) != 0 {
		t.Fatalf("nil applied must become empty slice: %#v", r)
	}
	one := Response([]AppliedEdit{{"type": "x"}})
	if len(one["applied_edits"].([]AppliedEdit)) != 1 {
		t.Fatal("applied kept")
	}
}

func TestCov_EstimateHelpers(t *testing.T) {
	// Invalid JSON falls back to len/3.
	if got := estimatePrompt([]byte(strings.Repeat("x", 30))); got != 10 {
		t.Fatalf("fallback: %d", got)
	}
	if estimatePrompt([]byte(`{"model":"m","messages":[{"role":"user","content":"hello world"}]}`)) <= 0 {
		t.Fatal("valid request must estimate > 0")
	}
	if jsonTokens(make(chan int)) != 0 {
		t.Fatal("unmarshalable -> 0")
	}
	if jsonTokens(map[string]string{"k": strings.Repeat("v", 100)}) <= 0 {
		t.Fatal("jsonTokens > 0")
	}
}
