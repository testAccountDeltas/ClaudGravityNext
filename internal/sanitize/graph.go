// Part of the sanitize package — see text.go for the package doc comment.
// This file ports v1's Step 4: tool_use/tool_result graph consistency.
package sanitize

import (
	"encoding/json"
	"fmt"

	"claudgravity/internal/protocol/claude"
)

// enforceToolGraphConsistency ports v1's Step 4 in its original two passes:
// first, a tool_use block sitting in a non-assistant turn (a buggy client,
// or corrupted history — Anthropic only ever puts a real tool_use in an
// assistant turn) is degraded to a plain text block, so every downstream
// consumer sees one consistent already-degraded shape instead of each
// needing its own role check; second, a tool_result with no matching
// assistant-role tool_use ANYWHERE in the conversation (truly orphaned,
// not just delayed — the delayed case is Step 5's job) is converted to
// text too, since Gemini has no functionCall for it to answer.
func enforceToolGraphConsistency(msgs []claude.Message) ([]claude.Message, bool) {
	modified := false
	toolUsesSeen := make(map[string]bool)

	// GAP-D4-04: this used to run as two full passes — the first
	// populating toolUsesSeen from EVERY message regardless of order,
	// the second checking every tool_result against that fully-built,
	// order-blind set. A tool_result physically appearing BEFORE its
	// own tool_use in the message array (corrupted/replayed history)
	// was wrongly recognized as matched, and got sent to Google as a
	// functionResponse with no preceding functionCall in its turn —
	// Gemini rejects that outright with a 400. A single forward pass,
	// registering each message's own tool_use IDs only AFTER that same
	// message's tool_results have already been checked against the map,
	// ensures a tool_result can only match a tool_use from a STRICTLY
	// EARLIER message, matching Gemini's own ordering requirement.
	out := make([]claude.Message, len(msgs))
	for mi, m := range msgs {
		newBlocks := make([]claude.Block, len(m.Content))
		var thisMsgToolUseIDs []string
		for bi, b := range m.Content {
			switch b.Type {
			case "tool_use", "server_tool_use":
				var tuID, tuName string
				if tu, ok := b.AsToolUse(); ok {
					tuID, tuName = tu.ID, tu.Name
				} else if su, ok := b.AsServerToolUse(); ok {
					tuID, tuName = su.ID, su.Name
				}
				if m.Role == claude.RoleAssistant {
					newBlocks[bi] = b
					if tuID != "" {
						thisMsgToolUseIDs = append(thisMsgToolUseIDs, tuID)
					}
					continue
				}
				newBlocks[bi] = claude.NewBlock(claude.TextBlock{
					Type: "text",
					Text: fmt.Sprintf("[%s %q in a non-assistant message — ignored]", b.Type, tuName),
				})
				modified = true
			case "tool_result", "web_search_tool_result":
				var trID string
				var trContent []byte
				if tr, ok := b.AsToolResult(); ok {
					trID, trContent = tr.ToolUseID, tr.Content
				} else if wr, ok := b.AsWebSearchToolResult(); ok {
					trID = wr.ToolUseID
					trContent, _ = json.Marshal(wr.Content)
				}
				if !toolUsesSeen[trID] {
					newBlocks[bi] = claude.NewBlock(claude.TextBlock{
						Type: "text",
						Text: orphanedToolResultText(trID, trContent),
					})
					modified = true
					continue
				}
				newBlocks[bi] = b
			default:
				newBlocks[bi] = b
			}
		}
		for _, id := range thisMsgToolUseIDs {
			toolUsesSeen[id] = true
		}
		out[mi] = claude.Message{Role: m.Role, Content: newBlocks, OutputConfig: m.OutputConfig}
	}

	return out, modified
}

func orphanedToolResultText(toolUseID string, content []byte) string {
	if txt := toolResultPlainText(content); txt != "" {
		return fmt.Sprintf("[Tool output %s]: %s", toolUseID, txt)
	}
	return fmt.Sprintf("[Tool output %s]", toolUseID)
}
