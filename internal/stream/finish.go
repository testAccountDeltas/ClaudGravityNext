// Part of the stream package — see events.go for the package doc
// comment. Direct port of v1's FinishStream — split from fsm.go purely to
// stay under the 400-line ceiling.
package stream

import (
	"encoding/json"
	"strings"
)

// FinishStream closes out the turn: closes any open content block,
// decides stop_reason (safety block / max_tokens / tool_use / recovered
// stop_sequence / end_turn, in that priority — computed independently of
// whether any content was actually emitted, so a MAX_TOKENS cutoff with
// zero content blocks still reports stop_reason:"max_tokens", not
// "end_turn"), emits the Anthropic minItems:1 fallback block if nothing
// was ever streamed, computes the real cache_creation/cache_read split,
// and sends message_delta + message_stop.
func (f *FSM) FinishStream(finishReason string) error {
	f.mu.Lock()
	if f.hasFinished {
		f.mu.Unlock()
		return nil
	}
	f.hasFinished = true
	f.mu.Unlock()

	f.appendGroundingSourcesFooter()

	isSafetyBlock := finishReason == "SAFETY" || finishReason == "RECITATION" || finishReason == "BLOCKLIST" ||
		finishReason == "PROHIBITED_CONTENT" || finishReason == "SPII" || finishReason == "MALICIOUS" ||
		finishReason == "LANGUAGE" || finishReason == "IMAGE_SAFETY"

	stopReason := "end_turn"
	switch {
	case isSafetyBlock:
		stopReason = "refusal"
	case finishReason == "MAX_TOKENS":
		stopReason = "max_tokens"
	case f.toolUseOccurred:
		stopReason = "tool_use"
	}

	matchedStopSequence := ""
	if stopReason == "end_turn" && len(f.stopSequences) > 0 {
		for _, seq := range f.stopSequences {
			if seq != "" && strings.HasSuffix(f.lastText, seq) {
				matchedStopSequence = seq
				stopReason = "stop_sequence"
				break
			}
		}
	}

	if !f.toolUseOccurred {
		if err := f.FlushTextHoldBuffer(matchedStopSequence); err != nil {
			return err
		}
	} else {
		f.textHoldBuffer = ""
	}

	if err := f.EnsureMessageStart(); err != nil {
		return err
	}
	if err := f.closeCurrentBlock(); err != nil {
		return err
	}
	f.flushPendingThinkingSignatureFallback()

	// GAP-16: Anthropic Messages API requires at least one visible content block (text or tool_use).
	// If currentIndex < 0 (nothing was emitted) OR only thinking was emitted (!f.toolUseOccurred && f.textChars == 0),
	// we must emit a text block with f.prefillText or fallbackText so the response is valid and prefill is preserved.
	if f.currentIndex < 0 || (!f.toolUseOccurred && f.textChars == 0) {
		f.maxStopLen = 0 // Disable hold-back buffer for final fallback block so it is emitted immediately
		// Anthropic Messages API requires at least one content block
		// (minItems: 1). If a thinking block or tool call was already
		// streamed, that's already satisfied and this must NEVER inject
		// synthetic dots ("...") which would be visible in Claude Code's
		// terminal output.
		// GAP-D1-06: HandleText below runs the generated text through
		// ApplyPrefill, which prepends f.prefillText once — concatenating
		// that onto a synthetic "(empty response)" placeholder produced
		// visible garbage like "{prefix}(empty response)" instead of the
		// Anthropic contract's own rule for this exact case (model
		// generated nothing beyond the prefix it was given to continue):
		// the returned text is simply the prefix itself, unmodified. Use
		// the real prefill text as the fallback content when one exists,
		// so ApplyPrefill's own already-starts-with-prefill check below
		// (HandleText -> ApplyPrefill) makes this a no-op rather than a
		// second concatenation.
		fallbackText := "(empty response)"
		if f.prefillText != "" {
			fallbackText = f.prefillText
		}
		if stopReason == "refusal" {
			fallbackText = "I'm not able to help with that request."
			f.prefillText = "" // GAP-R17-14: prevent ApplyPrefill from prepending prefill onto refusal text
		}
		if err := f.HandleText(fallbackText); err != nil {
			return err
		}
		if err := f.closeCurrentBlock(); err != nil {
			return err
		}
	}

	// Gemini's promptTokenCount is the FULL input (cached + uncached),
	// but Anthropic's input_tokens means only the uncached delta — cached
	// reads are reported separately via cache_read_input_tokens.
	if f.inputTokens == 0 && f.estInputTokens > 0 {
		f.inputTokens = int64(f.estInputTokens)
	}
	uncachedInputTokens := f.inputTokens - f.cacheTokens
	if uncachedInputTokens < 0 {
		uncachedInputTokens = 0
	}

	var cacheCreationTokens int64
	// Gated on the request actually having set a cache_control breakpoint
	// (cachePrefixFrac>0) — firing on every large cache-miss prompt
	// regardless of whether caching was ever requested would misreport a
	// plain request as one that paid for a cache write. Only the prefix
	// up to the breakpoint is actually written to cache; the current
	// turn's own new content after it stays plain input_tokens. Not
	// gated on cacheTokens==0: reading an early breakpoint and writing a
	// new one for additional fresh content later in the SAME turn are
	// not mutually exclusive.
	if uncachedInputTokens > cacheCreationThreshold(f.model) && f.cachePrefixFrac > 0 {
		cacheCreationTokens = int64(float64(uncachedInputTokens) * f.cachePrefixFrac)
		// Anthropic's formula: total prompt tokens = input_tokens +
		// cache_creation_input_tokens + cache_read_input_tokens — tokens
		// that created cache must NOT also be counted in input_tokens, or
		// SDKs double-count every token.
		uncachedInputTokens -= cacheCreationTokens
	}

	// Record this turn's real cache fraction for the NEXT turn's
	// message_start estimate (caches.go).
	if f.inputTokens > 0 {
		if f.cacheTokens > 0 {
			setSessionCacheFraction(f.sessionID, float64(f.cacheTokens)/float64(f.inputTokens))
		} else if cacheCreationTokens > 0 {
			// Turn 1 created cache for this prompt; turn 2 will see the
			// vast majority of it as a hit. Seed 0.85 so message_start on
			// turn 2 reflects that instead of latching 0%.
			setSessionCacheFraction(f.sessionID, 0.85)
		} else {
			// GAP-R17-25: cache miss resets session fraction
			setSessionCacheFraction(f.sessionID, 0.0)
		}
	}

	if f.outputTokens == 0 && f.textChars > 0 {
		est := int64(f.textChars / 4)
		if est < 1 {
			est = 1
		}
		f.outputTokens = est
	}

	// By Anthropic Messages API specification, message_delta.usage strictly
	// contains output_tokens (and output_tokens_details). input_tokens and
	// cache fields belong exclusively in message_start.
	usage := map[string]interface{}{
		"output_tokens": f.outputTokens,
	}
	// GAP-TH-07
	if f.thinkingTokens > 0 {
		usage["output_tokens_details"] = map[string]interface{}{
			"thinking_tokens": f.thinkingTokens,
		}
	}

	var stopSequenceField interface{}
	if matchedStopSequence != "" {
		stopSequenceField = matchedStopSequence
	}
	// GAP-P2-03: the non-streaming response path always includes
	// "stop_details" (null on a normal end_turn/max_tokens finish, a
	// populated object only for "refusal") — this streaming message_delta
	// used to OMIT the key entirely outside the refusal case, instead of
	// also sending it as null, an inconsistency a client comparing the
	// two response shapes (or a strict typed SDK expecting the field to
	// always be present) could trip over.
	var stopDetailsField interface{}
	if stopReason == "refusal" {
		stopDetailsField = map[string]interface{}{
			"type":        "refusal",
			"category":    "general_harms",
			"explanation": "Content blocked by safety policy",
		}
	}
	deltaMap := map[string]interface{}{
		"stop_reason":   stopReason,
		"stop_sequence": stopSequenceField,
		"stop_details":  stopDetailsField,
	}

	if f.requestedSafeguards {
		toolUsesMap := make(map[string]interface{})
		for _, tid := range f.toolCallIDs {
			toolUsesMap[tid] = map[string]interface{}{
				"type":    "evaluated",
				"outcome": "not_flagged",
			}
		}
		deltaMap["safeguard_results"] = []interface{}{
			map[string]interface{}{
				"type": "dangerous_tool_use",
				"status": map[string]interface{}{
					"type":      "available",
					"tool_uses": toolUsesMap,
				},
			},
		}
	}

	deltaPayload := map[string]interface{}{
		"type":  "message_delta",
		"delta": deltaMap,
		"usage": usage,
	}
	b, _ := json.Marshal(deltaPayload)
	if err := f.writer.EmitEvent("message_delta", string(b)); err != nil {
		return err
	}

	stopErr := f.writer.EmitEvent("message_stop", `{"type":"message_stop"}`)
	// An audit caught that nothing ever called f.writer.Finish() here —
	// StreamGeminiToClaude's read loop can have several Gemini SSE "data:"
	// lines sitting in the SAME read() buffer, and once this FinishStream
	// call returns, the loop keeps calling ProcessGeminiChunk on the
	// remaining lines with no guard (f.hasFinished above only prevents
	// FinishStream itself from running twice, not further HandleText/
	// HandleFunctionCall calls from a trailing chunk). Finish() makes every
	// later EmitEvent a no-op, so any such trailing chunk is silently
	// dropped instead of writing more SSE frames after message_stop and
	// corrupting the client's stream parser. See also the f.hasFinished
	// check added to ProcessGeminiChunk (chunk.go) for the other half of
	// this fix — this call alone wouldn't stop a trailing chunk from still
	// mutating FSM state (e.g. re-opening a content block) even though its
	// writes would now be swallowed.
	f.writer.Finish()
	return stopErr
}
