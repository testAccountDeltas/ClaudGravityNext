// Part of the sanitize package — see text.go for the package doc comment.
// This file ports v1's Step 1 (eager truncation + text normalization) and
// Step 3 (merge consecutive same-role messages).
package sanitize

import (
	"encoding/json"
	"strings"

	"claudgravity/internal/protocol/claude"
)

// normalizeAndTruncate ports v1's Step 1: strips NUL bytes and polluted
// tool-call narration from text, drops empty/hollow text blocks (a lone
// "." from an assistant is dropped; the same text as the message's ENTIRE
// bare-string content is replaced with "..." instead — v1 draws this exact
// distinction between the two shapes, so it survives here even though the
// typed domain otherwise erases the shape difference at decode time), and
// eager middle-truncates tool_result content over ToolOutputCharLimit.
func normalizeAndTruncate(msgs []claude.Message) ([]claude.Message, bool, bool) {
	modified := false
	truncatedAny := false
	newMsgs := make([]claude.Message, 0, len(msgs))

	for _, m := range msgs {
		role := m.Role
		if role == "" {
			role = claude.RoleUser
		}

		var normBlocks []claude.Block

		if m.ContentWasBareString && len(m.Content) == 1 {
			text, _ := m.Content[0].AsText()
			content := text.Text
			if strings.Contains(content, "\x00") {
				content = strings.ReplaceAll(content, "\x00", "")
				modified = true
			}
			// GAP-T13: only an assistant turn can emit the pseudo-tool-call
			// narration this pattern targets — scoping it to that role
			// avoids stripping a user's own pasted log/text that happens to
			// contain a literal "[Called tool ...]" substring.
			if role == claude.RoleAssistant {
				if cleaned := StripPollutedToolCallText(content); cleaned != content {
					content = cleaned
					modified = true
				}
			}
			if strings.TrimSpace(content) == "" || strings.TrimSpace(content) == "." {
				content = "..."
				modified = true
			}
			normBlocks = append(normBlocks, claude.NewBlock(claude.TextBlock{Type: "text", Text: content}))
		} else {
			for _, b := range m.Content {
				switch b.Type {
				case "text":
					newBlock, dropped, changed := normalizeTextBlock(b, role)
					if dropped {
						modified = true
						continue
					}
					if changed {
						modified = true
					}
					normBlocks = append(normBlocks, newBlock)
				case "tool_result":
					newBlock, didTruncate, didModify := truncateToolResult(b)
					if didModify {
						modified = true
					}
					if didTruncate {
						truncatedAny = true
					}
					normBlocks = append(normBlocks, newBlock)
				default:
					normBlocks = append(normBlocks, b)
				}
			}
			if len(normBlocks) == 0 {
				normBlocks = append(normBlocks, claude.NewBlock(claude.TextBlock{Type: "text", Text: "..."}))
				modified = true
			}
		}

		newMsgs = append(newMsgs, claude.Message{Role: role, Content: normBlocks, OutputConfig: m.OutputConfig})
	}

	return newMsgs, modified, truncatedAny
}

// normalizeTextBlock cleans one "text" block's NUL bytes and polluted
// tool-call narration, and reports whether it should be dropped entirely
// (empty, or a hollow "." from an assistant — matches v1's rule, which
// only drops the hollow-dot case for the assistant role, not user).
func normalizeTextBlock(b claude.Block, role claude.Role) (claude.Block, bool, bool) {
	txt, _ := b.AsText()
	text := txt.Text
	changed := false
	if cleaned, did := cleanTerminalText(text); did {
		text = cleaned
		changed = true
	}
	if role == claude.RoleAssistant {
		if cleaned := StripPollutedToolCallText(text); cleaned != text {
			text = cleaned
			changed = true
		}
	}
	if strings.TrimSpace(text) == "" || (role == claude.RoleAssistant && strings.TrimSpace(text) == ".") {
		return claude.Block{}, true, false
	}
	if !changed {
		return b, false, false
	}
	// GAP-T3: this reconstruction used to drop Citations, silently
	// stripping document citations from any text block that also needed
	// NUL-byte or polluted-narration cleanup.
	return claude.NewBlock(claude.TextBlock{Type: "text", Text: text, CacheControl: txt.CacheControl, Citations: txt.Citations}), false, true
}

// truncateToolResult eager-truncates a tool_result's text content (either
// a bare string, or the text/bare-string items inside an array — an
// image/document item in the array is left untouched, same as v1). Other
// fields (ToolUseID, IsError, CacheControl) are always preserved across
// the rebuild.
func truncateToolResult(b claude.Block) (claude.Block, bool, bool) {
	tr, ok := b.AsToolResult()
	if !ok {
		return b, false, false
	}

	var asString string
	if err := json.Unmarshal(tr.Content, &asString); err == nil {
		c := asString
		modified := false
		truncated := false
		if cleaned, did := cleanTerminalText(c); did {
			c = cleaned
			modified = true
		}
		if out, did := TruncateMiddle(c); did {
			c = out
			modified = true
			truncated = true
		}
		if !modified {
			return b, false, false
		}
		tr.Content, _ = json.Marshal(c)
		return claude.NewBlock(tr), truncated, true
	}

	var items []json.RawMessage
	if err := json.Unmarshal(tr.Content, &items); err != nil {
		// GAP-P1-05: tr.Content is neither a bare string nor a block
		// array — a tool_result content shape Anthropic's own spec
		// doesn't document, but some MCP/custom tool integrations emit
		// a raw JSON object directly (e.g. `{"result": {...huge...}}`).
		// Falling through untouched here let an arbitrarily large
		// object skip the 64KB cap entirely. Treat it the same as the
		// bare-string case: serialize, truncate, and store the result
		// back as a string — a valid tool_result content shape, and the
		// model still gets the (now-truncated) data rather than losing
		// it outright.
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(tr.Content, &obj); err != nil {
			return b, false, false
		}
		serialized := string(tr.Content)
		if out, did := TruncateMiddle(serialized); did {
			tr.Content, _ = json.Marshal(out)
			return claude.NewBlock(tr), true, true
		}
		return b, false, false
	}
	modified := false
	truncated := false
	newItems := make([]json.RawMessage, len(items))
	copy(newItems, items)
	for i, item := range items {
		var blk struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item, &blk); err == nil && blk.Type == "text" {
			pt := blk.Text
			cleaned, changedHere := cleanTerminalText(pt)
			pt = cleaned
			if out, did := TruncateMiddle(pt); did {
				pt = out
				changedHere = true
				truncated = true
			}
			if changedHere {
				modified = true
				var full map[string]json.RawMessage
				_ = json.Unmarshal(item, &full)
				full["text"], _ = json.Marshal(pt)
				newItems[i], _ = json.Marshal(full)
			}
			continue
		}
		var s string
		if err := json.Unmarshal(item, &s); err == nil {
			str := s
			cleaned, changedHere := cleanTerminalText(str)
			str = cleaned
			if out, did := TruncateMiddle(str); did {
				str = out
				changedHere = true
				truncated = true
			}
			if changedHere {
				modified = true
				newItems[i], _ = json.Marshal(str)
			}
		}
	}
	if !modified {
		return b, false, false
	}
	tr.Content, _ = json.Marshal(newItems)
	return claude.NewBlock(tr), truncated, true
}

// mergeConsecutiveSameRole ports v1's Step 3: two adjacent messages with
// the same role are folded into one, content blocks concatenated in
// order.
func mergeConsecutiveSameRole(msgs []claude.Message) ([]claude.Message, bool) {
	modified := false
	merged := make([]claude.Message, 0, len(msgs))
	for _, m := range msgs {
		if len(merged) > 0 && merged[len(merged)-1].Role == m.Role {
			last := &merged[len(merged)-1]
			last.Content = append(last.Content, m.Content...)
			modified = true
			continue
		}
		merged = append(merged, m)
	}
	return merged, modified
}
