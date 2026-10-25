// Part of the tokens package — see text.go for the package doc comment.
// Ports v1's TruncateConversationToFit (context_window.go), with one
// deliberate dependency-inversion change: v1 took a model NAME and looked
// up its real ceiling itself (globalModelCatalog, falling back to a
// hardcoded fallbackMaxTokens map) — a wrong-direction dependency on
// internal/accounts for this package. TruncateConversationToFit here
// takes the resolved maxTokens directly; the future internal/orchestrate
// package (which already owns accounts.ModelCatalog()) is responsible for
// resolving a model name to its ceiling — including v1's fallback-table
// safety net for a model present in neither the live catalog nor any
// hardcoded list — before calling this.
package tokens

import (
	"strings"

	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/sanitize"
)

// ContextSafetyMarginTokens reserves headroom beyond the reserved output
// budget, since this package's estimators are approximations, not the
// real tokenizer Google uses.
const ContextSafetyMarginTokens = 2000

// toolUseIDs returns the tool_use ids m's assistant-role tool_use blocks
// declare, or nil if m has none — used by GAP-D4-10's pairing check.
func toolUseIDs(m claude.Message) []string {
	if m.Role != claude.RoleAssistant {
		return nil
	}
	var ids []string
	for _, b := range m.Content {
		if tu, ok := b.AsToolUse(); ok && tu.ID != "" {
			ids = append(ids, tu.ID)
		} else if su, ok := b.AsServerToolUse(); ok && su.ID != "" {
			ids = append(ids, su.ID)
		}
	}
	return ids
}

// messageHasAnyToolResult reports whether m carries a tool_result or
// web_search_tool_result whose ToolUseID is one of ids.
func messageHasAnyToolResult(m claude.Message, ids []string) bool {
	for _, b := range m.Content {
		if tr, ok := b.AsToolResult(); ok {
			for _, id := range ids {
				if tr.ToolUseID == id {
					return true
				}
			}
		} else if wr, ok := b.AsWebSearchToolResult(); ok {
			for _, id := range ids {
				if wr.ToolUseID == id {
					return true
				}
			}
		}
	}
	return false
}

// TruncateConversationToFit drops the oldest intermediate messages (never
// index 0 — the user's original task statement — and never the last two,
// to preserve recent context) when the estimated total context would
// exceed maxTokens. req is never mutated in place; the returned Request is
// a new value when truncated is true, otherwise req itself.
func TruncateConversationToFit(req claude.Request, maxTokens int64, requestedMaxOutput int64, sessionIDs ...string) (claude.Request, int, bool) {
	if len(req.Messages) < 4 {
		// Too small to meaningfully trim, and dropping from a tiny history
		// risks breaking role alternation for no benefit.
		return req, 0, false
	}

	budget := maxTokens - requestedMaxOutput - ContextSafetyMarginTokens
	if budget <= 0 {
		budget = maxTokens / 2
	}

	systemTokens := ConservativeSystemTokens(req.System) + ConservativeToolsTokens(req.Tools)

	// Mirrors the translate path's own lastAssistantIdx: only the single
	// most recent assistant message's thinking survives to actually reach
	// Gemini, so only that one message should price its thinking at real
	// size — see blockCost's "thinking" case.
	lastAssistantIdx := -1
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == claude.RoleAssistant {
			lastAssistantIdx = i
			break
		}
	}

	// Virtual read deduplication during token budgeting: repeated Read
	// results within readDecayTurns are estimated at the collapsed stub cost
	// (~82 tokens) instead of full raw text, preventing premature eviction
	// of valuable conversation history on repeated reads.
	sessionID := ""
	if len(sessionIDs) > 0 {
		sessionID = sessionIDs[0]
	}
	budgetReq, _ := sanitize.DedupRepeatedReads(req, sessionID)

	msgTokens := make([]int, len(budgetReq.Messages))
	total := systemTokens
	for i, m := range budgetReq.Messages {
		msgTokens[i] = ConservativeMessageTokens(m, i == lastAssistantIdx) + 3 // per-message framing overhead
		total += msgTokens[i]
	}

	if total <= int(budget) {
		return req, 0, false
	}

	// First attempt to recover budget by shedding heavy image/document attachments
	// from older intermediate turns (keeping the last 3 turns intact) before
	// dropping whole conversational turns.
	if prunedMsgs, didPrune := pruneOldAttachments(req.Messages); didPrune {
		req.Messages = prunedMsgs
		budgetReq, _ = sanitize.DedupRepeatedReads(req, sessionID)
		total = systemTokens
		for i, m := range budgetReq.Messages {
			msgTokens[i] = ConservativeMessageTokens(m, i == lastAssistantIdx) + 3
			total += msgTokens[i]
		}
		if total <= int(budget) {
			return req, 0, true
		}
	}

	// Drop oldest INTERMEDIATE messages first, starting at index 1: index 0
	// is the user's original task statement, and erasing it leaves the
	// agent with no idea what it was asked to do.
	//
	// GAP-D4-10: internal/sanitize's own graph-consistency repair
	// (enforceToolGraphConsistency) runs BEFORE this function in the
	// real pipeline (translate.go: SanitizeMessagesAndGraph, then
	// TruncateConversationToFit — see internal/sanitize/dedup.go's own
	// GAP-SN2 comment establishing that exact order), not after — a
	// comment here used to claim the opposite. Dropping an assistant's
	// tool_use message while keeping its paired user tool_result message
	// (or vice versa) produces a FRESH orphaned pairing with nothing
	// downstream left to repair it, which Gemini rejects as an invalid
	// functionResponse/functionCall mismatch. Drop a tool_use message
	// and its immediately-following paired tool_result message together,
	// as one atomic unit, instead of one message at a time.
	dropped := 0
	i := 1
	for total > int(budget) && i < len(req.Messages)-2 {
		unitLen := 1
		ids := toolUseIDs(req.Messages[i])
		if len(ids) > 0 {
			// GAP-R17-04: Collect all subsequent messages answering ANY of these tool IDs to preserve atomic groups.
			// Must scan up to len(req.Messages) so answers in the protected tail are recognized.
			j := i + 1
			for j < len(req.Messages) && messageHasAnyToolResult(req.Messages[j], ids) {
				j++
			}
			unitLen = j - i
		}
		if i+unitLen > len(req.Messages)-2 {
			break
		}
		for u := 0; u < unitLen; u++ {
			total -= msgTokens[i]
			dropped++
			i++
		}
	}
	if dropped == 0 {
		return req, 0, false
	}

	// GAP-R17-03: Ensure the preserved tail begins with an assistant message to maintain User(0) -> Assistant(i) alternation
	for i < len(req.Messages)-2 && req.Messages[i].Role != claude.RoleAssistant {
		total -= msgTokens[i]
		dropped++
		i++
	}

	// GAP-R17-03: Append notice directly to the original user task content to prevent two consecutive user messages
	hasNotice := false
	for _, b := range req.Messages[0].Content {
		if tb, ok := b.AsText(); ok && strings.Contains(tb.Text, "Earlier intermediate messages were dropped") {
			hasNotice = true
			break
		}
	}
	if !hasNotice {
		noticeText := "\n\n[Gateway notice: Earlier intermediate messages were dropped to fit inside this conversation's context window. The conversation continues below.]"
		msg0Content := make([]claude.Block, len(req.Messages[0].Content), len(req.Messages[0].Content)+1)
		copy(msg0Content, req.Messages[0].Content)
		msg0Content = append(msg0Content, claude.NewBlock(claude.TextBlock{
			Type: "text",
			Text: noticeText,
		}))
		// Copy before writing: req is a by-value copy but its Messages slice
		// still shares the caller's backing array, and this function must
		// not mutate the caller's request.
		req.Messages = append([]claude.Message(nil), req.Messages...)
		req.Messages[0].Content = msg0Content
	}

	newMsgs := make([]claude.Message, 0, 2+len(req.Messages)-i)
	newMsgs = append(newMsgs, req.Messages[0])
	if i == len(req.Messages)-2 && req.Messages[i].Role == claude.RoleUser {
		newMsgs = append(newMsgs, claude.Message{
			Role: claude.RoleAssistant,
			Content: []claude.Block{
				claude.NewBlock(claude.TextBlock{
					Type: "text",
					Text: "I understand. Continuing with the task based on the context above.",
				}),
			},
		})
	}
	newMsgs = append(newMsgs, req.Messages[i:]...)
	req.Messages = newMsgs

	// Post-truncation safety re-check: dropping earlier reads may un-stub the
	// first surviving read (virtual dedup expansion), expanding it back to full
	// size. Re-verify against budget and continue dropping if needed.
	for len(req.Messages) >= 4 {
		evalReq, _ := sanitize.DedupRepeatedReads(req, sessionID)
		if ConservativeRequestTokens(evalReq) <= int(budget) {
			break
		}
		if len(req.Messages) <= 4 {
			break
		}
		// Drop next intermediate turn(s) starting at index 1
		unitLen := 1
		ids := toolUseIDs(req.Messages[1])
		if len(ids) > 0 {
			j := 2
			for j < len(req.Messages) && messageHasAnyToolResult(req.Messages[j], ids) {
				j++
			}
			unitLen = j - 1
		}
		if 1+unitLen > len(req.Messages)-2 {
			break
		}
		req.Messages = append(req.Messages[:1], req.Messages[1+unitLen:]...)
		dropped += unitLen
		// Maintain alternation if needed
		for len(req.Messages) > 2 && req.Messages[1].Role != claude.RoleAssistant {
			req.Messages = append(req.Messages[:1], req.Messages[2:]...)
			dropped++
		}
	}

	return req, dropped, true
}

// pruneOldAttachments sheds heavy image and document content blocks from older
// intermediate turns (preserving the initial task at index 0 and the last 3 turns)
// when attachments consume excessive context or risk exceeding the 20MB inlineData ceiling.
func pruneOldAttachments(msgs []claude.Message) ([]claude.Message, bool) {
	if len(msgs) < 4 {
		return msgs, false
	}
	cutoff := len(msgs) - 3
	changed := false
	out := make([]claude.Message, len(msgs))
	for i, m := range msgs {
		if i == 0 || i >= cutoff {
			out[i] = m
			continue
		}
		var newBlocks []claude.Block
		msgChanged := false
		for _, b := range m.Content {
			if b.Type == "image" || b.Type == "document" {
				newBlocks = append(newBlocks, claude.NewBlock(claude.TextBlock{
					Type: "text",
					Text: "[Previous image/document attachment omitted to fit context window]",
				}))
				msgChanged = true
				changed = true
			} else {
				newBlocks = append(newBlocks, b)
			}
		}
		if msgChanged {
			out[i] = claude.Message{Role: m.Role, Content: newBlocks}
		} else {
			out[i] = m
		}
	}
	return out, changed
}
