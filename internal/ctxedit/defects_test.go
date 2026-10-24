package ctxedit

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const clearEdit = `{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":1},"keep":{"type":"tool_uses","value":0}}`

func body(msgs, edit string) string {
	return `{"max_tokens":10,"messages":[` + msgs + `],"model":"m","context_management":{"edits":[` + edit + `]}}`
}

func use(id, name string) string {
	return fmt.Sprintf(`{"content":[{"id":"%s","input":{"a":1},"name":"%s","type":"tool_use"}],"role":"assistant"}`, id, name)
}

func res(id, content string) string {
	return fmt.Sprintf(`{"content":[{"content":%q,"tool_use_id":"%s","type":"tool_result"}],"role":"user"}`, content, id)
}

var big = strings.Repeat("data ", 300)

func TestDuplicateToolUseIDsCountedOnce(t *testing.T) {
	msgs := strings.Join([]string{`{"content":"go","role":"user"}`, use("d", "Read"), res("d", big), use("d", "Read"), res("d", big)}, ",")
	// one distinct tool use: trigger tool_uses>1 must not fire on the duplicate
	r := run(t, body(msgs, clearEdit))
	if len(r.Applied) != 0 {
		t.Fatalf("duplicate id counted twice: %v", r.Applied)
	}
	msgs += "," + use("e", "Read") + "," + res("e", big)
	r = run(t, body(msgs, clearEdit))
	if len(r.Applied) != 1 || r.Applied[0]["cleared_tool_uses"] != 2 {
		t.Fatalf("want 2 cleared (d,e), got %v", r.Applied)
	}
	// only the first d result is cleared, the second copy is not a pair
	if n := strings.Count(string(r.Body), ToolResultPlaceholder); n != 2 {
		t.Fatalf("placeholders: %d", n)
	}
}

func TestDuplicateIDCannotSatisfyClearAtLeast(t *testing.T) {
	msgs := strings.Join([]string{`{"content":"go","role":"user"}`, use("d", "Read"), res("d", "small"), use("d", "Read"), res("d", "small"), use("k", "Read"), res("k", "x")}, ",")
	e := `{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":1},"keep":{"type":"tool_uses","value":1},"clear_at_least":{"type":"input_tokens","value":50}}`
	if r := run(t, body(msgs, e)); len(r.Applied) != 0 {
		t.Fatalf("freed must not be double counted: %v", r.Applied)
	}
}

func TestOrphanAndStringContentUntouched(t *testing.T) {
	msgs := strings.Join([]string{
		`{"content":"plain string","role":"user"}`,
		res("ghost", big), // result with no tool_use
		use("late", "Read"),
		`{"content":"assistant string","role":"assistant"}`,
		use("a", "Read"), res("a", big),
		use("b", "Read"), res("b", big),
	}, ",")
	r := run(t, body(msgs, clearEdit))
	out := string(r.Body)
	if !strings.Contains(out, `"tool_use_id":"ghost"`) || strings.Count(out, big) != 1 {
		t.Fatalf("orphan result must stay intact (1 big copy): %d", strings.Count(out, big))
	}
	if !strings.Contains(out, `"plain string"`) || !strings.Contains(out, `"assistant string"`) {
		t.Fatal("string content messages must be untouched")
	}
	// result before its use
	msgs = strings.Join([]string{res("x", big), use("x", "Read"), use("y", "Read"), res("y", big)}, ",")
	r = run(t, body(msgs, clearEdit))
	if strings.Count(string(r.Body), big) != 1 {
		t.Fatalf("result preceding its tool_use must be left alone")
	}
}

func TestCacheControlSurvivesClearing(t *testing.T) {
	cc := `"cache_control":{"type":"ephemeral"}`
	msgs := strings.Join([]string{
		use("a", "Read"),
		fmt.Sprintf(`{"content":[{"content":%q,%s,"tool_use_id":"a","type":"tool_result"}],"role":"user"}`, big, cc),
		use("z", "Read"), res("z", "k"),
	}, ",")
	keep1 := `{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":1},"keep":{"type":"tool_uses","value":1}}`
	r := run(t, body(msgs, keep1))
	if gjson.GetBytes(r.Body, "messages.1.content.0.cache_control.type").String() != "ephemeral" ||
		gjson.GetBytes(r.Body, "messages.1.content.0.content").String() != ToolResultPlaceholder {
		t.Fatalf("cleared result keeps cache_control on the placeholder: %s", r.Body)
	}
	edit := `{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":1}}`
	th2 := `{"content":[{"signature":"s2","thinking":"hm","type":"thinking"},{"text":"hi","type":"text"}],"role":"assistant"}`
	// thinking-only turn: breakpoint moves to the placeholder text block
	th := fmt.Sprintf(`{"content":[{"signature":"s","thinking":"hm","type":"thinking",%s}],"role":"assistant"}`, cc)
	r = run(t, body(`{"content":"q","role":"user"},`+th+","+th2, edit))
	if gjson.GetBytes(r.Body, "messages.1.content.0.text").String() != ThinkingPlaceholder ||
		gjson.GetBytes(r.Body, "messages.1.content.0.cache_control.type").String() != "ephemeral" {
		t.Fatalf("placeholder must carry cache_control: %s", r.Body)
	}
	// mixed turn: moves to the last remaining block
	th = fmt.Sprintf(`{"content":[{"signature":"s","thinking":"hm","type":"thinking",%s},{"text":"yo","type":"text"}],"role":"assistant"}`, cc)
	r = run(t, body(`{"content":"q","role":"user"},`+th+","+th2, edit))
	if strings.Count(string(r.Body), "cache_control") != 1 || gjson.GetBytes(r.Body, "messages.1.content.0.cache_control.type").String() != "ephemeral" {
		t.Fatalf("breakpoint count must be unchanged: %s", r.Body)
	}
}

func TestApplyLeavesOtherBytesLiteral(t *testing.T) {
	tail := `{"content":[{"text":"a<b>&c 12345678901234567890.50","type":"text"}],"role":"user"}`
	msgs := strings.Join([]string{`{"content":"x<y & z>","role":"user"}`, use("a", "Read"), res("a", big), use("b", "Read"), res("b", "k"), tail}, ",")
	r := run(t, body(msgs, `{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":1},"keep":{"type":"tool_uses","value":1}}`))
	if len(r.Applied) != 1 {
		t.Fatalf("not applied: %v", r.Applied)
	}
	out := string(r.Body)
	for _, want := range []string{`"x<y & z>"`, tail, use("b", "Read"), res("b", "k")} {
		if !strings.Contains(out, want) {
			t.Fatalf("bytes outside edited block changed, missing %s in %s", want, out)
		}
	}
	if strings.Contains(out, "\\"+"u003c") || strings.Contains(out, "\\"+"u0026") {
		t.Fatalf("HTML escaping leaked: %s", out)
	}
}

func TestStripField(t *testing.T) {
	in := `{"model":"m","context_management":{"edits":[]},"max_tokens":1}`
	out := string(StripField([]byte(in)))
	if strings.Contains(out, "context_management") || !strings.Contains(out, `"max_tokens":1`) {
		t.Fatalf("got %s", out)
	}
	in = `{"model":"m"}`
	if string(StripField([]byte(in))) != in {
		t.Fatal("absent field must leave the body unchanged")
	}
}

func TestTriggerTokensVsToolUsesAndKeepZero(t *testing.T) {
	msgs := strings.Join([]string{`{"content":"go","role":"user"}`, use("a", "Read"), res("a", big), use("b", "Read"), res("b", big)}, ",")
	hi := `{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":900000},"keep":{"type":"tool_uses","value":0}}`
	lo := `{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":10},"keep":{"type":"tool_uses","value":0}}`
	if r := run(t, body(msgs, hi)); len(r.Applied) != 0 {
		t.Fatalf("high token trigger fired: %v", r.Applied)
	}
	r := run(t, body(msgs, lo))
	if len(r.Applied) != 1 || r.Applied[0]["cleared_tool_uses"] != 2 {
		t.Fatalf("keep 0 must clear everything: %v", r.Applied)
	}
	eq := `{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":2},"keep":{"type":"tool_uses","value":0}}`
	if r := run(t, body(msgs, eq)); len(r.Applied) != 0 {
		t.Fatalf("trigger must be strictly exceeded: %v", r.Applied)
	}
}

func TestExcludeToolsAndClearInputs(t *testing.T) {
	msgs := strings.Join([]string{`{"content":"go","role":"user"}`, use("a", "Keepme"), res("a", big), use("b", "Read"), res("b", big)}, ",")
	e := `{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":1},"keep":{"type":"tool_uses","value":0},"exclude_tools":["Keepme"],"clear_tool_inputs":true}`
	r := run(t, body(msgs, e))
	if len(r.Applied) != 1 || r.Applied[0]["cleared_tool_uses"] != 1 {
		t.Fatalf("applied: %v", r.Applied)
	}
	if gjson.GetBytes(r.Body, "messages.2.content.0.content").String() != big {
		t.Fatal("excluded tool result must stay")
	}
	if gjson.GetBytes(r.Body, "messages.1.content.0.input.a").Int() != 1 {
		t.Fatal("excluded tool input must stay")
	}
	if gjson.GetBytes(r.Body, "messages.3.content.0.input").Raw != "{}" {
		t.Fatalf("cleared tool input must be blanked: %s", r.Body)
	}
}
