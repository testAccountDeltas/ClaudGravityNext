// Part of the orchestrate package — see pipeline.go for the package doc
// comment. Non-streaming half of gateway-side stop_sequences matching (the
// streaming half is internal/stream/stopseq.go): Gemini is not sent the
// sequences, so the finished response is scanned for the earliest one.
package orchestrate

import (
	"math"
	"strings"

	"claudgravity/internal/tokens"
)

// applyStopSequences finds the earliest client stop sequence across the
// response's text blocks in order. On a hit the text is cut right before
// it, every later block is dropped (the turn ended there), stop_reason
// becomes "stop_sequence" with the matched sequence, and output_tokens is
// re-estimated for the kept text (Gemini generated past the match and
// counted all of it). A refusal is left alone. enforceMaxTokens runs
// afterwards, so a max_tokens cap reached before the match still wins.
func applyStopSequences(claudeResp map[string]interface{}, usage map[string]interface{}, seqs []string, thinkingTok int64) {
	if len(seqs) == 0 || claudeResp["stop_reason"] == "refusal" {
		return
	}
	blocks, ok := claudeResp["content"].([]map[string]interface{})
	if !ok {
		return
	}
	for i, b := range blocks {
		if b["type"] == "tool_use" {
			return // text after a tool call is dropped; the turn ends at the call
		}
		if b["type"] != "text" {
			continue
		}
		txt, _ := b["text"].(string)
		idx, seq := -1, ""
		for _, cand := range seqs {
			if j := strings.Index(txt, cand); cand != "" && j >= 0 && (idx < 0 || j < idx || (j == idx && len(cand) > len(seq))) {
				idx, seq = j, cand
			}
		}
		if seq == "" {
			continue
		}
		kept := append([]map[string]interface{}{}, blocks[:i]...)
		if idx > 0 {
			b["text"] = txt[:idx]
			kept = append(kept, b)
		}
		claudeResp["content"] = kept
		claudeResp["stop_reason"] = "stop_sequence"
		claudeResp["stop_sequence"] = seq
		var visible float64
		for _, kb := range kept {
			if kb["type"] == "text" {
				t, _ := kb["text"].(string)
				visible += tokens.WeightedCharTokensFrac(t)
			}
		}
		if _, has := usage["output_tokens"].(int64); has {
			usage["output_tokens"] = thinkingTok + int64(math.Max(1, math.Ceil(visible)))
		}
		return
	}
}
