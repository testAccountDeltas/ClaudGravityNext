package ctxedit

import (
	"fmt"
	"strings"
	"testing"
)

func conv(pairs int, think bool) string {
	var msgs []string
	msgs = append(msgs, `{"role":"user","content":"start"}`)
	for i := 1; i <= pairs; i++ {
		th := ""
		if think {
			th = fmt.Sprintf(`{"type":"thinking","thinking":"%s","signature":"s%d"},`, strings.Repeat("hmm ", 50), i)
		}
		name := "Read"
		if i == 2 {
			name = "WebSearch"
		}
		msgs = append(msgs,
			fmt.Sprintf(`{"role":"assistant","content":[%s{"type":"tool_use","id":"t%d","name":"%s","input":{"p":"%d"}}]}`, th, i, name, i),
			fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d","content":"%s"}]}`, i, strings.Repeat("data ", 400)))
	}
	return `{"model":"claude-sonnet-4-5","max_tokens":10,"messages":[` + strings.Join(msgs, ",") + `],"context_management":{"edits":[%s]}}`
}

func run(t *testing.T, body string) Result {
	t.Helper()
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	r, err := Apply([]byte(body), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClearToolUsesKeepAndExclude(t *testing.T) {
	b := fmt.Sprintf(conv(6, false), `{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":100},"keep":{"type":"tool_uses","value":2},"exclude_tools":["WebSearch"]}`)
	r := run(t, b)
	if len(r.Applied) != 1 || r.Applied[0]["cleared_tool_uses"] != 3 {
		t.Fatalf("applied: %v", r.Applied)
	}
	// results of t1,t3,t4 cleared; t2 (excluded) and t5,t6 (kept) intact
	body := string(r.Body)
	if strings.Count(body, ToolResultPlaceholder) != 3 || strings.Count(body, `"content":"data `) != 3 {
		t.Fatalf("placeholder count wrong: %d / intact %d", strings.Count(body, ToolResultPlaceholder), strings.Count(body, `"content":"data `))
	}
	if r.Tokens >= r.OriginalTokens {
		t.Fatalf("tokens not reduced: %d -> %d", r.OriginalTokens, r.Tokens)
	}
	if !strings.Contains(body, `"input":{"p":"1"}`) {
		t.Fatal("tool inputs must stay by default")
	}
}

func TestClearToolInputsAndTriggers(t *testing.T) {
	b := fmt.Sprintf(conv(5, false), `{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":4},"keep":{"type":"tool_uses","value":1},"clear_tool_inputs":true}`)
	r := run(t, b)
	if len(r.Applied) != 1 || r.Applied[0]["cleared_tool_uses"] != 4 {
		t.Fatalf("applied: %v", r.Applied)
	}
	if strings.Contains(string(r.Body), `"input":{"p":"1"}`) || !strings.Contains(string(r.Body), `"input":{"p":"5"}`) {
		t.Fatal("old inputs must be blanked, kept input intact")
	}
	// below trigger: nothing
	b = fmt.Sprintf(conv(3, false), `{"type":"clear_tool_uses_20250919","trigger":{"type":"tool_uses","value":4}}`)
	if r := run(t, b); len(r.Applied) != 0 || string(r.Body) != b {
		t.Fatalf("below trigger must not edit: %v", r.Applied)
	}
	// default trigger 100k tokens not reached
	b = fmt.Sprintf(conv(8, false), `{"type":"clear_tool_uses_20250919"}`)
	if r := run(t, b); len(r.Applied) != 0 {
		t.Fatalf("default trigger must not fire on a small prompt: %v", r.Applied)
	}
}

func TestClearAtLeast(t *testing.T) {
	b := fmt.Sprintf(conv(5, false), `{"type":"clear_tool_uses_20250919","trigger":{"type":"input_tokens","value":10},"keep":{"type":"tool_uses","value":4},"clear_at_least":{"type":"input_tokens","value":5000}}`)
	if r := run(t, b); len(r.Applied) != 0 {
		t.Fatalf("clear_at_least not met must skip: %v", r.Applied)
	}
}

func TestClearThinking(t *testing.T) {
	b := fmt.Sprintf(conv(4, true), `{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":1}}`)
	r := run(t, b)
	if len(r.Applied) != 1 || r.Applied[0]["cleared_thinking_turns"] != 3 {
		t.Fatalf("applied: %v", r.Applied)
	}
	if n := strings.Count(string(r.Body), `"type":"thinking"`); n != 1 || !strings.Contains(string(r.Body), `"signature":"s4"`) {
		t.Fatalf("only the last turn's thinking must remain, have %d", n)
	}
	b = fmt.Sprintf(conv(4, true), `{"type":"clear_thinking_20251015","keep":"all"}`)
	if r := run(t, b); len(r.Applied) != 0 {
		t.Fatal("keep all must be a no-op")
	}
}

func TestParseValidation(t *testing.T) {
	bad := []string{
		`{"type":"clear_tool_uses_20250919"},{"type":"clear_thinking_20251015"}`,
		`{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":0}}`,
		`{"type":"clear_tool_uses_20250919","trigger":{"type":"bogus","value":1}}`,
		`{"type":"clear_tool_uses_20250919","keep":{"type":"tool_uses","value":-1}}`,
	}
	for _, e := range bad {
		if _, err := Parse([]byte(fmt.Sprintf(conv(1, false), e))); err == nil {
			t.Errorf("want error for %s", e)
		}
	}
	// unknown strategies are skipped, not rejected
	if _, err := Parse([]byte(fmt.Sprintf(conv(1, false), `{"type":"compact_20990101"}`))); err != nil {
		t.Fatal(err)
	}
	if c, _ := Parse([]byte(`{"messages":[]}`)); c.Present {
		t.Fatal("absent context_management must not be Present")
	}
}
