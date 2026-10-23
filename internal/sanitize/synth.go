// Part of the sanitize package — see text.go for the package doc comment.
// This file ports v1's Step 5: synthesizing recovery tool_results for
// abandoned tool_use calls, immediately after the assistant turn that made
// them (Gemini requires each functionCall to be closed by its
// functionResponse in the very next turn), and reordering real/synthetic
// results to match the original tool_use order.
package sanitize

import (
	"encoding/json"
	"fmt"

	"claudgravity/internal/protocol/claude"
)

func mustMarshal(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// synthesizeAbandonedToolResults ports v1's synthesis/reorder step.
func synthesizeAbandonedToolResults(msgs []claude.Message) ([]claude.Message, bool) {
	modified := false
	var rebuilt []claude.Message
	skipNext := false

	for i, m := range msgs {
		if skipNext {
			skipNext = false
			continue
		}
		rebuilt = append(rebuilt, m)

		if m.Role != claude.RoleAssistant {
			continue
		}

		var toolUseIDs []string
		for _, b := range m.Content {
			if b.Type == "tool_use" {
				if tu, ok := b.AsToolUse(); ok && tu.ID != "" {
					toolUseIDs = append(toolUseIDs, tu.ID)
				}
			}
		}

		nextIsUser := i+1 < len(msgs) && msgs[i+1].Role == claude.RoleUser

		nextTurnResultIDs := make(map[string]bool)
		if nextIsUser {
			for _, b := range msgs[i+1].Content {
				if tr, ok := b.AsToolResult(); ok && tr.ToolUseID != "" {
					nextTurnResultIDs[tr.ToolUseID] = true
				} else if wr, ok := b.AsWebSearchToolResult(); ok && wr.ToolUseID != "" {
					nextTurnResultIDs[wr.ToolUseID] = true
				}
			}
		}

		var synths []claude.Block
		synthByID := make(map[string]claude.Block)
		for _, id := range toolUseIDs {
			if nextTurnResultIDs[id] {
				continue
			}
			s := claude.NewBlock(claude.ToolResultBlock{
				Type:      "tool_result",
				ToolUseID: id,
				IsError:   true,
				Content:   mustMarshal("(recovered by proxy)"),
			})
			synths = append(synths, s)
			synthByID[id] = s
		}

		if len(toolUseIDs) == 0 {
			hasStrayResult := false
			if nextIsUser {
				for _, b := range msgs[i+1].Content {
					if b.Type == "tool_result" {
						hasStrayResult = true
						break
					}
				}
			}
			if !hasStrayResult {
				continue
			}
		}

		if len(synths) == 0 && !nextIsUser {
			continue
		}

		modified = true

		if !nextIsUser {
			rebuilt = append(rebuilt, claude.Message{Role: claude.RoleUser, Content: synths})
			continue
		}

		nextBlocks := msgs[i+1].Content
		realResults := make(map[string]claude.Block, len(nextBlocks))
		var otherBlocks []claude.Block
		for _, b := range nextBlocks {
			if b.Type == "tool_result" {
				tr, _ := b.AsToolResult()
				if tr.ToolUseID != "" {
					realResults[tr.ToolUseID] = b
					continue
				}
			}
			otherBlocks = append(otherBlocks, b)
		}

		ordered := make([]claude.Block, 0, len(nextBlocks)+len(synths))
		consumed := make(map[string]bool, len(realResults))
		for _, id := range toolUseIDs {
			if real, ok := realResults[id]; ok {
				ordered = append(ordered, real)
				consumed[id] = true
			} else if synth, ok := synthByID[id]; ok {
				ordered = append(ordered, synth)
			}
		}

		// A tool_result answering a call from further back than the
		// immediately preceding assistant turn can't be emitted as a live
		// functionResponse (Gemini requires turn N's functionResponses to
		// match turn N-1's functionCalls) — degrade it to text instead.
		// Iterate nextBlocks (not the realResults map) to preserve
		// original insertion order, same reasoning as v1's own comment on
		// why it doesn't iterate the map directly.
		for _, b := range nextBlocks {
			if b.Type != "tool_result" {
				continue
			}
			tr, _ := b.AsToolResult()
			if tr.ToolUseID == "" || consumed[tr.ToolUseID] {
				continue
			}
			ordered = append(ordered, claude.NewBlock(claude.TextBlock{
				Type: "text",
				Text: delayedToolResultText(tr.ToolUseID, tr.Content),
			}))
		}

		ordered = append(ordered, otherBlocks...)
		rebuilt = append(rebuilt, claude.Message{Role: claude.RoleUser, Content: ordered})
		skipNext = true
	}

	return rebuilt, modified
}

func delayedToolResultText(toolUseID string, content []byte) string {
	if txt := toolResultPlainText(content); txt != "" {
		return fmt.Sprintf("[Delayed tool output for %s]: %s", toolUseID, txt)
	}
	return fmt.Sprintf("[Delayed tool output for %s]", toolUseID)
}
