package ctxedit

import "claudgravity/internal/tokens"

func isThinking(b map[string]interface{}) bool {
	t, _ := b["type"].(string)
	return t == "thinking" || t == "redacted_thinking"
}

// clearThinking drops thinking blocks from assistant turns older than the
// last KeepTurns turns that carry thinking.
func clearThinking(msgs []interface{}, e ThinkingEdit) (AppliedEdit, bool) {
	if e.KeepAll || e.KeepTurns < 1 {
		return nil, false
	}
	var withThinking []int
	for i, m := range msgs {
		mm := asMap(m)
		if mm == nil || mm["role"] != "assistant" {
			continue
		}
		blocks, _ := mm["content"].([]interface{})
		for _, b := range blocks {
			if bm := asMap(b); bm != nil && isThinking(bm) {
				withThinking = append(withThinking, i)
				break
			}
		}
	}
	if len(withThinking) <= e.KeepTurns {
		return nil, false
	}
	cleared, freed := 0, 0
	for _, i := range withThinking[:len(withThinking)-e.KeepTurns] {
		mm := asMap(msgs[i])
		blocks, _ := mm["content"].([]interface{})
		kept := make([]interface{}, 0, len(blocks))
		var cc interface{} // cache_control of a removed block
		for _, b := range blocks {
			if bm := asMap(b); bm != nil && isThinking(bm) {
				freed += jsonTokens(bm)
				if v, ok := bm["cache_control"]; ok {
					cc = v
				}
				continue
			}
			kept = append(kept, b)
		}
		if len(kept) == 0 {
			kept = append(kept, map[string]interface{}{"type": "text", "text": ThinkingPlaceholder})
		}
		// Keep the breakpoint count unchanged: a cache_control on a removed
		// block moves to the last remaining block unless that has its own.
		if last := asMap(kept[len(kept)-1]); cc != nil && last != nil {
			if _, has := last["cache_control"]; !has {
				last["cache_control"] = cc
			}
		}
		mm["content"] = kept
		cleared++
	}
	return AppliedEdit{
		"type":                   TypeClearThinking,
		"cleared_thinking_turns": cleared,
		"cleared_input_tokens":   freed,
	}, true
}

type toolPair struct {
	use    map[string]interface{}
	result map[string]interface{}
}

// clearToolUses replaces the content of the oldest tool results (all but
// the last Keep pairs) with a placeholder once the trigger is exceeded.
func clearToolUses(msgs []interface{}, e ToolUsesEdit, promptTokens int) (AppliedEdit, bool) {
	var order []string
	pairs := map[string]*toolPair{}
	for _, m := range msgs {
		mm := asMap(m)
		if mm == nil {
			continue
		}
		blocks, _ := mm["content"].([]interface{})
		for _, b := range blocks {
			bm := asMap(b)
			if bm == nil {
				continue
			}
			switch bm["type"] {
			case "tool_use":
				// First occurrence of an id wins; a duplicate must not be
				// counted (or cleared) twice.
				if id, _ := bm["id"].(string); id != "" && pairs[id] == nil {
					order = append(order, id)
					pairs[id] = &toolPair{use: bm}
				}
			case "tool_result":
				if id, _ := bm["tool_use_id"].(string); pairs[id] != nil && pairs[id].result == nil {
					// A result seen before its tool_use never matches (the
					// pair does not exist yet), so orphans stay untouched.
					pairs[id].result = bm
				}
			}
		}
	}
	n := len(order)
	if e.TriggerToolUses > 0 {
		if n <= e.TriggerToolUses {
			return nil, false
		}
	} else if promptTokens <= e.TriggerTokens {
		return nil, false
	}
	if n <= e.Keep {
		return nil, false
	}
	placeholderCost := tokens.WeightedCharTokens(ToolResultPlaceholder)
	var targets []*toolPair
	freed := 0
	for _, id := range order[:n-e.Keep] {
		p := pairs[id]
		if p.result == nil || e.ExcludeTools[stringOf(p.use["name"])] {
			continue
		}
		if s, ok := p.result["content"].(string); ok && s == ToolResultPlaceholder {
			continue
		}
		gain := jsonTokens(p.result["content"]) - placeholderCost
		if e.ClearInputs {
			gain += jsonTokens(p.use["input"])
		}
		if gain <= 0 {
			continue
		}
		freed += gain
		targets = append(targets, p)
	}
	if len(targets) == 0 || (e.ClearAtLeast > 0 && freed < e.ClearAtLeast) {
		return nil, false
	}
	for _, p := range targets {
		p.result["content"] = ToolResultPlaceholder
		if e.ClearInputs {
			p.use["input"] = map[string]interface{}{}
		}
	}
	return AppliedEdit{
		"type":                 TypeClearToolUses,
		"cleared_tool_uses":    len(targets),
		"cleared_input_tokens": freed,
	}, true
}

func stringOf(v interface{}) string {
	s, _ := v.(string)
	return s
}
