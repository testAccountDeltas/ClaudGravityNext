// Part of the orchestrate package — see pipeline.go for the package doc
// comment. Non-streaming half of the client max_tokens contract (the
// streaming half is internal/stream/visiblecap.go): Gemini's
// maxOutputTokens is widened for hidden thinking, so the visible text is
// clipped back to the client's max_tokens here.
package orchestrate

import (
	"claudgravity/internal/stream"
	"claudgravity/internal/tokens"
)

// applyVisibleCap clips the response's text blocks to capTokens estimated
// tokens in order (thinking is never counted; tool_use blocks are exempt
// and kept whole). It does nothing while upstream's exact visible count
// (candTokens = candidatesTokenCount, excluding thoughts) proves the answer
// fits. Reports whether any text was cut.
func applyVisibleCap(claudeResp map[string]interface{}, capTokens, candTokens int64) bool {
	if capTokens <= 0 || (candTokens > 0 && candTokens <= capTokens) {
		return false
	}
	blocks, ok := claudeResp["content"].([]map[string]interface{})
	if !ok {
		return false
	}
	remaining := float64(capTokens)
	trimmed := false
	kept := make([]map[string]interface{}, 0, len(blocks))
	for _, b := range blocks {
		if b["type"] != "text" {
			kept = append(kept, b)
			continue
		}
		txt, _ := b["text"].(string)
		out, cut := stream.TrimToTokenBudget(txt, remaining)
		remaining -= tokens.WeightedCharTokensFrac(out)
		if remaining < 0 {
			remaining = 0
		}
		if cut {
			trimmed = true
		}
		if out == "" {
			continue
		}
		b["text"] = out
		kept = append(kept, b)
	}
	claudeResp["content"] = kept
	return trimmed
}

// enforceMaxTokens applies the cap and keeps stop_reason and
// usage.output_tokens consistent with it: a clipped answer is max_tokens
// (unless a tool call ends the turn) and output_tokens never exceeds
// max_tokens plus the thinking tokens reported alongside.
func enforceMaxTokens(claudeResp map[string]interface{}, usage map[string]interface{}, capTokens, candTokens, thinkingTok int64, hasToolUse bool) {
	trimmed := applyVisibleCap(claudeResp, capTokens, candTokens)
	if trimmed && claudeResp["stop_reason"] != "tool_use" && claudeResp["stop_reason"] != "refusal" {
		claudeResp["stop_reason"] = "max_tokens"
		claudeResp["stop_sequence"] = nil
	}
	if capTokens <= 0 || hasToolUse {
		return
	}
	limit := capTokens + thinkingTok
	if cur, _ := usage["output_tokens"].(int64); trimmed || cur > limit {
		usage["output_tokens"] = limit
	}
}
