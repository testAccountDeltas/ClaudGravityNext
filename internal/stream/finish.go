// Part of the stream package — see events.go for the package doc
// comment. Direct port of v1's FinishStream — split from fsm.go purely to
// stay under the 400-line ceiling.
package stream

import (
	"encoding/json"

	"claudgravity/internal/cacheusage"
)

// FinishStream closes out the turn. It first decides whether the turn is a
// usable answer at all (ClassifyFinishReason / FinishClass.CheckTurn): an
// abnormal finishReason (MALFORMED_FUNCTION_CALL, OTHER, ...) or an empty
// turn is NOT reported as a clean end_turn — it fails with an
// *UpstreamTurnError (retried by the orchestrator while nothing has reached
// the client, an SSE error event otherwise). For a usable turn it closes
// any open content block, decides stop_reason (safety block / max_tokens /
// tool_use / stop_sequence / end_turn, computed independently of whether
// content was emitted, so a MAX_TOKENS cutoff with zero content blocks
// still reports "max_tokens"), computes the real cache_creation/cache_read
// split, and sends message_delta + message_stop.
//
// No placeholder text is ever injected: Anthropic's own API returns
// content:[] for a refusal or max_tokens-before-any-output (the streaming
// refusal classifier routinely ends a stream with zero content blocks), and
// the old "(empty response)" / "I'm not able to help" fillers were stored by
// clients as real assistant turns and resent upstream. The one remaining
// synthesized text is the CLIENT's own assistant-prefill echo.
func (f *FSM) FinishStream(finishReason string) error {
	f.mu.Lock()
	if f.hasFinished {
		f.mu.Unlock()
		return nil
	}
	f.hasFinished = true
	f.mu.Unlock()

	f.finalFinishReason = finishReason
	cls := ClassifyFinishReason(finishReason)
	hasText := f.textChars > 0 || f.lastText != "" || f.textHoldBuffer != ""
	prefillOnly := f.prefillText != "" && !f.toolUseOccurred && !hasText &&
		cls.StopReason == "end_turn" && !cls.ToolFailure
	if cls.StopReason == "max_tokens" && f.retryEmptyMaxToken && !f.capReached && !hasText && !f.toolUseOccurred &&
		(f.thinkingTokens > 0 || f.thinkingChars > 0) {
		// MAX_TOKENS with nothing visible: hidden thinking ate the whole
		// output allowance. Not an answer; the orchestrator retries ONCE
		// with a widened allowance before this is reported as max_tokens.
		f.finalStopReason = "error"
		return f.failTurn(&UpstreamTurnError{ErrType: "overloaded_error", Status: statusForErrType("overloaded_error"),
			FinishReason: finishReason, NeedsMoreOutput: true,
			Msg: "upstream hit the output token limit while thinking, before any visible output"})
	}
	if !prefillOnly {
		if terr := cls.CheckTurn(finishReason, hasText, f.toolUseOccurred || f.droppedToolCalls > 0); terr != nil {
			f.finalStopReason = "error"
			return f.failTurn(terr)
		}
	}
	if err := f.writer.Release(); err != nil {
		return err
	}

	if cls.StopReason != "refusal" && cls.StopReason != "max_tokens" {
		f.appendGroundingSourcesFooter()
	}

	stopReason := cls.StopReason
	if stopReason == "end_turn" && f.toolUseOccurred {
		stopReason = "tool_use"
	}

	// stop_sequence: the gateway matches client sequences itself over the
	// whole text stream (HandleText); Gemini never sees them. A last scan
	// of the not-yet-emitted tail covers a match only visible after the
	// final sanitize/flush.
	matchedStopSequence := f.stopMatched
	if matchedStopSequence == "" && stopReason == "end_turn" && !f.toolUseOccurred {
		if _, seq := f.findStopSequence(f.textHoldBuffer); seq != "" {
			matchedStopSequence = seq
		}
	}
	if matchedStopSequence != "" && stopReason == "end_turn" {
		stopReason = "stop_sequence"
	} else if stopReason != "stop_sequence" {
		matchedStopSequence = ""
	}

	if !f.toolUseOccurred {
		if err := f.FlushTextHoldBuffer(matchedStopSequence); err != nil {
			return err
		}
	} else {
		f.textHoldBuffer = ""
	}
	if f.capReached && stopReason != "refusal" && stopReason != "tool_use" {
		// The final flush spent the client's max_tokens: the answer was
		// cut by the cap, not ended by the model or a stop sequence.
		stopReason = "max_tokens"
		matchedStopSequence = ""
	}

	if err := f.EnsureMessageStart(); err != nil {
		return err
	}
	if err := f.closeCurrentBlock(); err != nil {
		return err
	}
	f.flushPendingThinkingSignatureFallback()

	// The client's own assistant prefill is real content (Anthropic's
	// contract: the response continues the prefix), so a turn that produced
	// nothing beyond it still returns the prefix. Never a placeholder.
	if f.prefillText != "" && !f.prefillEmitted && !f.toolUseOccurred && stopReason != "refusal" && f.textChars == 0 {
		f.maxStopLen = 0 // emit immediately, no hold-back
		if err := f.HandleText(f.prefillText); err != nil {
			return err
		}
		if err := f.closeCurrentBlock(); err != nil {
			return err
		}
	}
	f.finalStopReason = stopReason

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

	// Only the prefix up to the last breakpoint is written to cache, and
	// only when upstream read nothing from cache and the prefix reaches
	// the model's minimum (cacheusage.Split never invents numbers).
	cc5m, cc1h := cacheusage.Split(f.model, uncachedInputTokens, f.cacheTokens, f.cachePrefixFrac, f.cacheFrac1h)
	cacheCreationTokens := cc5m + cc1h
	// Anthropic's formula: total = input + cache_creation + cache_read.
	uncachedInputTokens -= cacheCreationTokens

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

	f.clampOutputToCap(stopReason)
	if f.outputTokens == 0 && f.textChars > 0 {
		est := int64(f.textChars / 4)
		if est < 1 {
			est = 1
		}
		f.outputTokens = est
	}

	// message_delta.usage is cumulative and SDK accumulators take the last
	// non-null value of each field, so it carries the REAL token counts
	// (message_start only had a pre-stream estimate): uncached input,
	// cache_creation, cache_read — the same accounting as the non-streaming
	// response — plus output.
	usage := map[string]interface{}{
		"input_tokens":                uncachedInputTokens,
		"output_tokens":               f.outputTokens,
		"cache_creation_input_tokens": cacheCreationTokens,
		"cache_creation":              cacheusage.Object(cc5m, cc1h),
		"cache_read_input_tokens":     f.cacheTokens,
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
		explanation := "Content blocked by safety policy"
		if f.refusalDetail != "" {
			explanation += " (" + f.refusalDetail + ")"
		} else if finishReason != "" {
			explanation += " (finishReason=" + finishReason + ")"
		}
		stopDetailsField = map[string]interface{}{
			"type":        "refusal",
			"category":    "general_harms",
			"explanation": explanation,
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
	if f.contextManagement != nil {
		deltaPayload["context_management"] = f.contextManagement
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
