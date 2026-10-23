// Part of the translate package — see translate.go for the package doc
// comment. Direct port of v1's translator.go "thinking"/
// "redacted_thinking" content-block cases inside Step 5's message loop.
package translate

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/toolsig"
)

// convertThinkingBlock converts one "thinking" content block. Only the
// single most recent assistant message's thinking is kept (isLastAssistantMsg);
// every earlier one is dropped — Anthropic's own spec only requires
// preserving a thinking block for continuity within an ACTIVE tool-use
// loop, and resending every historical thought on every request just
// accumulates context bloat over a long agentic session for no benefit.
// content is the enclosing message's full content array, searched for a
// sibling tool_use whose real signature internal/stream cached against
// its own tool_use id, recovering it even when the live stream already
// closed this thinking block with the skip-validator placeholder before
// that functionCall's real signature arrived.
func convertThinkingBlock(p gjson.Result, role string, isLastAssistantMsg bool, content gjson.Result, isAntigravity bool, sessionID string) []string {
	thoughtTxt := p.Get("thinking").String()
	if thoughtTxt == "" {
		return nil
	}
	if role != "assistant" {
		// Anthropic's spec never puts a "thinking" block in a user
		// message, but a looser client could still send one — Google
		// rejects "thought" outright on a non-model turn, so render it as
		// plain text instead of risking a 400 on the whole request over
		// one stray block.
		part := `{"text":""}`
		part, _ = sjson.Set(part, "text", thoughtTxt)
		return []string{part}
	}
	if !isLastAssistantMsg {
		return nil
	}

	// GAP-CGN-D1-06: thoughts from prior turns are only valid to preserve reasoning state
	// within an ACTIVE tool-use loop. On finished text turns (where content has text but no
	// tool_use), strip thoughts to avoid context bloat and reasoning degradation.
	if content.IsArray() && len(content.Array()) > 0 {
		hasText := false
		hasToolUse := false
		content.ForEach(func(_, sibling gjson.Result) bool {
			switch sibling.Get("type").String() {
			case "tool_use", "server_tool_use":
				hasToolUse = true
			case "text":
				if strings.TrimSpace(sibling.Get("text").String()) != "" {
					hasText = true
				}
			}
			return true
		})
		if hasText && !hasToolUse {
			return nil
		}
	}

	if !isAntigravity {
		// The public Gemini Developer API (generativelanguage.googleapis.com,
		// direct API key — !isAntigravity) rejects a request whose history
		// contains a part with "thought":true at all: "Invalid
		// Content.parts[0]: thought field is not supported in request
		// contents". Antigravity's internal CloudCode endpoint accepts it
		// (and benefits from the real signature for reasoning continuity),
		// so this degradation only applies on the direct-API-key path —
		// render the same text as a plain visible block instead of a real
		// thought part.
		part := `{"text":""}`
		part, _ = sjson.Set(part, "text", thoughtTxt)
		return []string{part}
	}

	// Prefer the real signature the client echoed back over the
	// skip-validator bypass; Google warns that permanently skipping the
	// validator can degrade reasoning quality on long thinking chains.
	sig := p.Get("signature").String()
	if sig == "skip_thought_signature_validator" {
		sig = ""
	}
	if sig == "" {
		// In a thought->text->functionCall sequence split across
		// SSE chunks, the live stream closes the thinking block with the
		// skip-validator placeholder before the LATER functionCall's real
		// thoughtSignature ever arrives, but that real signature IS
		// cached against the functionCall's own tool_use id. On REPLAY (a
		// later turn), the sibling tool_use this same assistant message
		// carries is right here in `content` — check it for a real cached
		// signature before giving up and resending the same placeholder
		// forever. Unlike buffering the live stream itself, this only
		// runs once, on the next turn, with no latency cost at all.
		content.ForEach(func(_, sibling gjson.Result) bool {
			sibType := sibling.Get("type").String()
			if sibType != "tool_use" && sibType != "server_tool_use" {
				return true
			}
			if real := toolsig.Get(sibling.Get("id").String()); real != "" {
				sig = real
				return false
			}
			return true
		})
		// GAP-TH-06: no sibling tool_use had a real signature cached
		// (this turn had no tool call at all) — try the session-level
		// "last real signature seen" fallback before giving up to the
		// skip-validator placeholder.
		if sig == "" {
			sig = toolsig.GetSessionSignature(sessionID)
		}
		if sig == "" {
			sig = "skip_thought_signature_validator"
		}
	}
	part := `{"thought":true,"thoughtSignature":"","text":""}`
	part, _ = sjson.Set(part, "thoughtSignature", sig)
	part, _ = sjson.Set(part, "text", thoughtTxt)
	return []string{part}
}

// convertRedactedThinkingBlock converts a "redacted_thinking" block —
// opaque and encrypted, with nothing real to translate. Keeps the turn's
// structure intact with a placeholder instead of silently dropping it
// (which could otherwise leave an assistant turn with zero parts). Only
// the most recent assistant turn's redacted_thinking keeps the real
// thought:true treatment — same "only the last turn's thinking survives"
// rule genuine "thinking" blocks get, extended here too since an older
// one has no genuine signature to preserve either way (always the same
// skip_thought_signature_validator placeholder) and so no reason to ever
// treat it as thought:true.
func convertRedactedThinkingBlock(role string, isLastAssistantMsg bool, isAntigravity bool) []string {
	if !isAntigravity || role != "assistant" || !isLastAssistantMsg {
		return []string{`{"text":"[Prior reasoning redacted]"}`}
	}
	return []string{`{"thought":true,"thoughtSignature":"skip_thought_signature_validator","text":"[Prior reasoning redacted]"}`}
}
