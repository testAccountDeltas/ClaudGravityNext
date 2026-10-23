// Part of the sanitize package — see text.go for the package doc comment.
// This file holds the top-level entry point and the two remaining steps
// (leading role:"system" message extraction, and the trailing-assistant
// prefill nudge) that don't warrant their own file.
package sanitize

import (
	"encoding/json"
	"fmt"
	"strings"

	"claudgravity/internal/protocol/claude"
)

// Result is SanitizeMessagesAndGraph's output — one struct instead of
// v1's 3-value return tuple, per the migration spec's diagnosis (Part 1.4)
// of multi-return tuples as a recurring source of a caller silently
// dropping or misreading one of the values.
type Result struct {
	Request      claude.Request
	Modified     bool
	TruncatedAny bool
	// PrefillText is the assistant-prefill text (if any) extracted from
	// the trailing assistant turn found by this call — needed so the
	// non-streaming executor can honor Anthropic's prefill contract (the
	// returned completion must begin with the text the client itself
	// supplied as the last assistant turn); Gemini has no real prefill
	// mechanism, so the gateway must prepend it itself.
	PrefillText string
}

// SanitizeMessagesAndGraph performs AST-level pre-flight sanitization on
// req's conversation messages, matching v1's sanitizer.go function of the
// same name step for step:
//  1. Eager middle-truncates tool_results (>64KB).
//  2. Removes empty/whitespace text blocks with a fallback sentinel.
//  3. Ensures the first message has role "user".
//  4. Merges consecutive messages of the same role.
//  5. Converts orphaned tool_results (without tool_use) into plain text.
//  6. Synthesizes recovery responses for abandoned/unanswered tool_use calls.
//  7. Collapses repeated identical Read tool_results.
//  8. Appends a prefill-continuation nudge if the conversation ends on an
//     assistant turn.
//
// req is never mutated in place; Result.Request is a new value built from
// it (the migration spec's Part 1.3 diagnosis: v1's in-place mutation of
// shared map/slice aliases was itself a recurring source of bugs — two
// different call sites holding what they each believed was their own
// private copy of claudeReqJSON, one silently stepping on the other's
// edits).
func SanitizeMessagesAndGraph(req claude.Request) Result {
	if len(req.Messages) == 0 {
		return Result{Request: req}
	}

	modified := false

	msgs, sysModified := extractLeadingSystemMessages(&req)
	modified = modified || sysModified

	// GAP-D4-05: extractLeadingSystemMessages only strips role:"system"
	// messages at the very front — a MID-conversation one (a client
	// interleaving system reminders between turns) was left untouched,
	// breaking the alternating user/assistant pairing every downstream
	// step (mergeConsecutiveSameRole, enforceToolGraphConsistency,
	// synthesizeAbandonedToolResults) assumes, and could trigger a
	// fabricated "(recovered by proxy)" response from the pairing logic
	// misreading the system turn as a missing assistant reply.
	msgs, midSysModified := normalizeMidConversationSystemMessages(msgs)
	modified = modified || midSysModified

	if len(msgs) == 0 {
		// A request whose "messages" array held ONLY system-role message(s)
		// is genuinely empty after extraction — Gemini requires at least
		// one real content turn. The real instruction already made it into
		// req.System above.
		req.Messages = []claude.Message{{
			Role:    claude.RoleUser,
			Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: "Continue."})},
		}}
		return Result{Request: req, Modified: true}
	}

	newMsgs, step1Modified, truncatedAny := normalizeAndTruncate(msgs)
	modified = modified || step1Modified

	if len(newMsgs) == 0 {
		return Result{Request: req}
	}

	if newMsgs[0].Role != claude.RoleUser {
		newMsgs = append([]claude.Message{continueUserTurn()}, newMsgs...)
		modified = true
	}

	newMsgs, mergedChanged := mergeConsecutiveSameRole(newMsgs)
	modified = modified || mergedChanged

	newMsgs, graphChanged := enforceToolGraphConsistency(newMsgs)
	modified = modified || graphChanged

	newMsgs, synthChanged := synthesizeAbandonedToolResults(newMsgs)
	modified = modified || synthChanged

	// Dedup is deliberately NOT applied here — see DedupRepeatedReads's own
	// doc comment (GAP-SN2): it must run exactly once, as the caller's
	// LAST step before serialization, after every truncation pass has
	// already decided which messages survive, or a collapsed "see above"
	// marker can end up pointing at content a later truncation pass
	// deleted.

	var prefillText string
	if len(newMsgs) > 0 && newMsgs[len(newMsgs)-1].Role == claude.RoleAssistant {
		rawPre := extractPlainText(newMsgs[len(newMsgs)-1].Content)
		trimmed := strings.TrimSpace(rawPre)
		if trimmed == "" || trimmed == "..." || trimmed == "." {
			prefillText = ""
			newMsgs = append(newMsgs, continueUserTurn())
			modified = true
		} else {
			prefillText = rawPre
			newMsgs = append(newMsgs, prefillNudgeTurn(prefillText))
			modified = true
		}
	}

	req.Messages = newMsgs
	return Result{Request: req, Modified: modified, TruncatedAny: truncatedAny, PrefillText: prefillText}
}

func continueUserTurn() claude.Message {
	return claude.Message{
		Role:    claude.RoleUser,
		Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: "Continue."})},
	}
}

// prefillNudgeTurn builds the trailing user turn Gemini needs when the
// conversation ends on an assistant turn (Anthropic allows this — a
// prefill continuation request — but Gemini's generateContent requires
// ending on "user"). Gemini has no real prefill mechanism, so telling it
// the exact prefix text and to continue from it without repeating it is
// the best available approximation — same wording v1 uses.
func prefillNudgeTurn(prefillText string) claude.Message {
	nudgeText := "Continue."
	if prefillText != "" {
		nudgeText = fmt.Sprintf(
			"[SYSTEM NOTE: Your previous turn ended mid-output with this exact text, which the user already has: %q — continue DIRECTLY from the next character. Do not repeat, restate, or add any preamble before it; your reply must pick up exactly where that text left off.]",
			prefillText,
		)
	}
	return claude.Message{
		Role:    claude.RoleUser,
		Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: nudgeText})},
	}
}

// extractLeadingSystemMessages ports v1's leading-role:"system" extraction
// loop: a client built against the OpenAI-style convention can send
// messages[0] (and, per a Round-15 audit fix in v1, any number of
// consecutive further leading messages) with role "system" — Anthropic's
// real API only allows "system" in the separate top-level field. Loops
// while the new head is still role:"system", same as v1.
func extractLeadingSystemMessages(req *claude.Request) ([]claude.Message, bool) {
	msgs := req.Messages
	modified := false
	for len(msgs) > 0 && msgs[0].Role == claude.Role("system") {
		sysText := strings.TrimSpace(extractPlainText(msgs[0].Content))
		if sysText != "" {
			req.System = appendSystemText(req.System, sysText)
		}
		msgs = msgs[1:]
		modified = true
	}
	return msgs, modified
}

// normalizeMidConversationSystemMessages converts any role:"system"
// message NOT at the front of the array (those are already handled by
// extractLeadingSystemMessages) into role:"user", prefixing its text so
// the model can still tell it apart from a genuine user turn.
func normalizeMidConversationSystemMessages(msgs []claude.Message) ([]claude.Message, bool) {
	modified := false
	out := make([]claude.Message, 0, len(msgs))
	// carry holds an output_config (effort) from a dropped empty system
	// message until the adjacent turn can take it over.
	var carry json.RawMessage
	for _, m := range msgs {
		if m.Role != claude.Role("system") {
			if len(carry) > 0 && !hasOutputConfig(m.OutputConfig) {
				m.OutputConfig = carry
			}
			carry = nil
			out = append(out, m)
			continue
		}
		modified = true
		text := strings.TrimSpace(extractPlainText(m.Content))
		if text == "" {
			// An empty system message (content "", [] or [{text:""}]) must
			// not become a bare "[System Note]: " user turn. Keep only the
			// per-turn output_config it may carry.
			if hasOutputConfig(m.OutputConfig) {
				carry = m.OutputConfig
			}
			continue
		}
		out = append(out, claude.Message{
			Role:         claude.RoleUser,
			Content:      []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: "[System Note]: " + text})},
			OutputConfig: m.OutputConfig,
		})
	}
	// Nothing followed the dropped message: hand its output_config to the
	// preceding turn instead.
	if len(carry) > 0 && len(out) > 0 && !hasOutputConfig(out[len(out)-1].OutputConfig) {
		out[len(out)-1].OutputConfig = carry
	}
	return out, modified
}

func hasOutputConfig(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t != "" && t != "null"
}

// appendSystemText merges sysText into an existing SystemPrompt, whichever
// shape it's already in (array of blocks gets a new block appended, a
// string gets concatenated onto), or sets it fresh — matches v1's own
// three-way handling in sanitizer.go.
func appendSystemText(sys claude.SystemPrompt, sysText string) claude.SystemPrompt {
	if len(sys) > 0 {
		var arr []json.RawMessage
		if err := json.Unmarshal(sys, &arr); err == nil {
			newBlock, _ := json.Marshal(map[string]string{"type": "text", "text": sysText})
			arr = append(arr, newBlock)
			out, _ := json.Marshal(arr)
			return claude.SystemPrompt(out)
		}
		var s string
		if err := json.Unmarshal(sys, &s); err == nil && s != "" {
			out, _ := json.Marshal(s + "\n\n" + sysText)
			return claude.SystemPrompt(out)
		}
	}
	out, _ := json.Marshal(sysText)
	return claude.SystemPrompt(out)
}
