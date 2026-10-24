// Package ctxedit implements Anthropic's server-side context editing
// (request field context_management.edits) for a Gemini upstream that has
// no such feature: the gateway itself clears old tool results and old
// thinking blocks before translation, and reports what it did in the
// response's context_management.applied_edits.
//
// Spec: platform.claude.com/docs/en/docs/build-with-claude/context-editing
// (strategies clear_tool_uses_20250919 and clear_thinking_20251015).
package ctxedit

import (
	"fmt"

	"github.com/tidwall/gjson"
)

const (
	TypeClearToolUses = "clear_tool_uses_20250919"
	TypeClearThinking = "clear_thinking_20251015"

	// Defaults from the spec.
	DefaultTriggerTokens = 100000
	DefaultKeepToolUses  = 3
)

// ToolUsesEdit is a parsed clear_tool_uses_20250919 strategy.
type ToolUsesEdit struct {
	TriggerTokens   int // 0 when the trigger is counted in tool uses
	TriggerToolUses int // 0 when the trigger is counted in tokens
	Keep            int // most recent tool use/result pairs preserved
	ClearAtLeast    int // minimum input tokens a run must free; 0 = none
	ExcludeTools    map[string]bool
	ClearInputs     bool // also blank the tool_use input
}

// ThinkingEdit is a parsed clear_thinking_20251015 strategy.
type ThinkingEdit struct {
	KeepAll   bool
	KeepTurns int // assistant turns whose thinking is preserved (>0)
}

// Config is the validated context_management object.
type Config struct {
	Present  bool // the request carried context_management at all
	Thinking *ThinkingEdit
	ToolUses *ToolUsesEdit
}

// Parse validates context_management from a request body. Unknown edit
// types are skipped (a newer Claude Code may send strategies this gateway
// does not know; rejecting them would break the client).
func Parse(body []byte) (Config, error) {
	cm := gjson.GetBytes(body, "context_management")
	if !cm.Exists() || cm.Type == gjson.Null {
		return Config{}, nil
	}
	cfg := Config{Present: true}
	if !cm.IsObject() {
		return cfg, fmt.Errorf("context_management: must be an object")
	}
	edits := cm.Get("edits")
	if !edits.Exists() {
		return cfg, nil
	}
	if !edits.IsArray() {
		return cfg, fmt.Errorf("context_management.edits: must be an array")
	}
	for i, e := range edits.Array() {
		switch e.Get("type").String() {
		case TypeClearThinking:
			if cfg.ToolUses != nil {
				return cfg, fmt.Errorf("context_management.edits.%d: %s must come before %s", i, TypeClearThinking, TypeClearToolUses)
			}
			t, err := parseThinking(e)
			if err != nil {
				return cfg, fmt.Errorf("context_management.edits.%d: %v", i, err)
			}
			cfg.Thinking = t
		case TypeClearToolUses:
			t, err := parseToolUses(e)
			if err != nil {
				return cfg, fmt.Errorf("context_management.edits.%d: %v", i, err)
			}
			cfg.ToolUses = t
		}
	}
	return cfg, nil
}

func parseThinking(e gjson.Result) (*ThinkingEdit, error) {
	keep := e.Get("keep")
	if !keep.Exists() || keep.Type == gjson.Null {
		// Spec default is model dependent; every current model class keeps
		// all prior thinking.
		return &ThinkingEdit{KeepAll: true}, nil
	}
	if keep.Type == gjson.String {
		if keep.String() != "all" {
			return nil, fmt.Errorf("keep: must be \"all\" or {\"type\":\"thinking_turns\",\"value\":N}")
		}
		return &ThinkingEdit{KeepAll: true}, nil
	}
	if keep.Get("type").String() != "thinking_turns" {
		return nil, fmt.Errorf("keep.type: must be \"thinking_turns\"")
	}
	n, ok := positiveInt(keep.Get("value"))
	if !ok {
		return nil, fmt.Errorf("keep.value: must be an integer greater than 0")
	}
	return &ThinkingEdit{KeepTurns: n}, nil
}

func parseToolUses(e gjson.Result) (*ToolUsesEdit, error) {
	t := &ToolUsesEdit{TriggerTokens: DefaultTriggerTokens, Keep: DefaultKeepToolUses}
	if v := e.Get("trigger"); v.Exists() && v.Type != gjson.Null {
		n, ok := positiveInt(v.Get("value"))
		if !ok {
			return nil, fmt.Errorf("trigger.value: must be an integer greater than 0")
		}
		switch v.Get("type").String() {
		case "input_tokens":
			t.TriggerTokens = n
		case "tool_uses":
			t.TriggerTokens, t.TriggerToolUses = 0, n
		default:
			return nil, fmt.Errorf("trigger.type: must be \"input_tokens\" or \"tool_uses\"")
		}
	}
	if v := e.Get("keep"); v.Exists() && v.Type != gjson.Null {
		if v.Get("type").String() != "tool_uses" {
			return nil, fmt.Errorf("keep.type: must be \"tool_uses\"")
		}
		n := v.Get("value")
		if !n.Exists() || n.Type != gjson.Number || n.Int() < 0 || float64(n.Int()) != n.Num {
			return nil, fmt.Errorf("keep.value: must be an integer >= 0")
		}
		t.Keep = int(n.Int())
	}
	if v := e.Get("clear_at_least"); v.Exists() && v.Type != gjson.Null {
		n, ok := positiveInt(v.Get("value"))
		if !ok || v.Get("type").String() != "input_tokens" {
			return nil, fmt.Errorf("clear_at_least: must be {\"type\":\"input_tokens\",\"value\":N}")
		}
		t.ClearAtLeast = n
	}
	if v := e.Get("exclude_tools"); v.Exists() && v.Type != gjson.Null {
		if !v.IsArray() {
			return nil, fmt.Errorf("exclude_tools: must be an array of tool names")
		}
		t.ExcludeTools = map[string]bool{}
		for _, n := range v.Array() {
			t.ExcludeTools[n.String()] = true
		}
	}
	t.ClearInputs = e.Get("clear_tool_inputs").Bool()
	return t, nil
}

func positiveInt(v gjson.Result) (int, bool) {
	if !v.Exists() || v.Type != gjson.Number || v.Int() < 1 || float64(v.Int()) != v.Num {
		return 0, false
	}
	return int(v.Int()), true
}

// AppliedEdit is one entry of context_management.applied_edits.
type AppliedEdit map[string]interface{}

// Response builds the context_management object for a response.
func Response(applied []AppliedEdit) map[string]interface{} {
	if applied == nil {
		applied = []AppliedEdit{}
	}
	return map[string]interface{}{"applied_edits": applied}
}
