// Part of the orchestrate package — see pipeline.go for the package doc
// comment. Split out of nonstreaming.go purely to stay under the
// 400-line ceiling: the two response-assembly helpers that don't belong
// to the main candidate-walk.
package orchestrate

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"claudgravity/internal/loopguard"
	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/ratelimit"
	"claudgravity/internal/reqctx"
	"claudgravity/internal/speccapture"
	"claudgravity/internal/stream"
	"claudgravity/internal/tokens"
)

// applyPrefillToResponse re-attaches the client's assistant-prefill
// prefix to the response's own text block, via the SAME shared rule the
// streaming path's FSM.ApplyPrefill uses (internal/stream/prefill.go) —
// the structural fix for GAP-D1-02 (the streaming path's own copy of
// this check was forgotten/miswired in v1; only non-streaming had it).
// Looks for the first "text" block rather than assuming content[0],
// since content[0] is the "thinking" block whenever extended thinking is
// on.
func applyPrefillToResponse(claudeResp map[string]interface{}, prefillText string) {
	if prefillText == "" {
		return
	}
	contentBlocks, ok := claudeResp["content"].([]map[string]interface{})
	if !ok {
		return
	}
	emitted := false
	for _, b := range contentBlocks {
		if t, ok := b["type"].(string); ok && t == "text" {
			if existing, ok := b["text"].(string); ok {
				b["text"] = stream.PrependPrefillOnce(prefillText, existing, &emitted)
			}
			emitted = true
			return
		}
	}
	// GAP-D1-05/GAP-D4-03: if the model's response is tool_use-only (no
	// "text" block at all — e.g. it immediately called a tool), the loop
	// above found nothing and the prefill was silently dropped with no
	// fallback. Per the Anthropic prefill contract, synthesize a text
	// block holding the prefix and insert it before the first tool_use,
	// rather than lose the client's prefix entirely.
	insertAt := len(contentBlocks)
	for i, b := range contentBlocks {
		if t, _ := b["type"].(string); t == "tool_use" {
			insertAt = i
			break
		}
	}
	prefillBlock := map[string]interface{}{"type": "text", "text": prefillText}
	newBlocks := make([]map[string]interface{}, 0, len(contentBlocks)+1)
	newBlocks = append(newBlocks, contentBlocks[:insertAt]...)
	newBlocks = append(newBlocks, prefillBlock)
	newBlocks = append(newBlocks, contentBlocks[insertAt:]...)
	claudeResp["content"] = newBlocks
}

// stripOscIgnoreTag removes the internal loopguard tag ("[ignoring loop detection]")
// from text blocks so it is never exposed to the client or recorded in conversation history.
func stripOscIgnoreTag(claudeResp map[string]interface{}) {
	contentBlocks, ok := claudeResp["content"].([]map[string]interface{})
	if !ok {
		return
	}
	for _, b := range contentBlocks {
		if t, ok := b["type"].(string); ok && t == "text" {
			if existing, ok := b["text"].(string); ok && loopguard.HasIgnoreLoopTag(existing) {
				b["text"] = loopguard.StripIgnoreLoopTag(existing)
			}
		}
	}
}

// writeBlockedResponse builds the same stop_reason:"refusal" message
// object the streaming path (internal/stream's FinishStream) produces
// for a prompt blocked before any candidate exists — a prompt-level
// promptFeedback.blockReason, as opposed to a per-candidate finishReason.
// A 200 with writeAnthropicError's {"type":"error",...} body is rejected
// outright by official Anthropic SDKs (a 200 status means "parse this as
// a message object").
func writeBlockedResponse(p *reqctx.Params, st *reqctx.State, accLabel string, claudeBodyBytes []byte, blockReason string, w http.ResponseWriter) (bool, int, error) {
	var req claude.Request
	_ = json.Unmarshal(claudeBodyBytes, &req)
	respModel := req.Model
	if respModel == "" {
		respModel = st.ResolvedModelName
	}
	blockResp := map[string]interface{}{
		"id":      fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		"type":    "message",
		"role":    "assistant",
		"model":   respModel,
		"content": []map[string]interface{}{{"type": "text", "text": "I am unable to complete this request because it was flagged by upstream safety filters: " + blockReason}},
		"usage": map[string]interface{}{
			"input_tokens":                tokens.WeightedRequestTokens(req),
			"output_tokens":               0,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
		},
		"stop_reason":   "refusal",
		"stop_sequence": nil,
		"stop_details": map[string]interface{}{
			"type":        "refusal",
			"category":    "general_harms",
			"explanation": "Content blocked by safety policy",
		},
	}
	blockJSON, _ := json.Marshal(blockResp)
	ratelimit.ApplyHeaders(w.Header(), st.ActiveAccount, st.ResolvedModelName)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(blockJSON)
	speccapture.Global().RecordClaudeResponse(p.RID, w.Header(), blockJSON)
	return true, http.StatusOK, nil
}
