// Part of the orchestrate package — see pipeline.go for the package doc
// comment. Split out of nonstreaming.go purely to stay under the
// 400-line ceiling: the candidate.content.parts walk (thinking/text/
// functionCall) that v1's executeNonStreaming inlined directly in its
// own body.
package orchestrate

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/editrepair"
	"claudgravity/internal/langdetect"
	"claudgravity/internal/logx"
	"claudgravity/internal/loopguard"
	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/reqctx"
	"claudgravity/internal/schema"
	"claudgravity/internal/stream"
	"claudgravity/internal/toolargs"
	"claudgravity/internal/toolcallid"
	"claudgravity/internal/toolsig"
)

// candidateResult is processCandidate's output: everything
// runNonStreamingAttempt needs for LoopGuard bookkeeping and the
// request log, mirroring the local variables v1's executeNonStreaming
// accumulated inline.
type candidateResult struct {
	toolsCalled        []string
	toolCallIDs        []string
	toolCallSignatures []string
	droppedCalls       int // calls Gemini made although the request forbids them
	textChars          int
	thinkingChars      int
	lastText           string
	lastThinking       string
}

// processCandidate walks candidates.0.content.parts, appending to
// claudeResp["content"] in place, and returns the bookkeeping
// candidateResult needs. cand may be the zero gjson.Result (no
// candidate at all — a genuinely empty response).
func processCandidate(p *reqctx.Params, cand gjson.Result, claudeResp map[string]interface{}, claudeBodyBytes []byte, req claude.Request, cwdKey, resolvedModel string) candidateResult {
	var cr candidateResult
	var lastTurnThoughtSig string
	if !cand.Exists() {
		return cr
	}

	parts := cand.Get("content.parts")
	if parts.IsArray() {
		// Gemini commonly attaches a thought's signature to the
		// functionCall part that FOLLOWS it, not to the thought part
		// itself — pre-scan the whole candidate once so a later part's
		// signature is available up front, independent of visitation
		// order.
		var candidateThoughtSig string
		for _, part := range parts.Array() {
			if s := part.Get("thoughtSignature").String(); s != "" {
				candidateThoughtSig = s
				break
			}
			if s := part.Get("functionCall.thoughtSignature").String(); s != "" {
				candidateThoughtSig = s
				break
			}
		}
		for _, part := range parts.Array() {
			partSig := part.Get("thoughtSignature").String()
			if partSig == "" {
				partSig = part.Get("functionCall.thoughtSignature").String()
			}
			if partSig != "" {
				lastTurnThoughtSig = partSig
			}
			switch {
			case part.Get("thought").Bool():
				t := part.Get("text").String()
				cr.thinkingChars += len(t)
				cr.lastThinking += t
				sig := partSig
				if sig == "" {
					sig = candidateThoughtSig
				}
				if sig == "" {
					sig = lastTurnThoughtSig
				}
				if sig == "" {
					sig = toolsig.GetSessionSignature(cwdKey, resolvedModel)
				}
				if sig != "" {
					toolsig.SetSessionSignature(cwdKey, sig, resolvedModel)
				}
				appendThinkingBlock(claudeResp, t, sig)

			case part.Get("text").String() != "":
				t := part.Get("text").String()
				if len(cr.toolsCalled) > 0 {
					// Anthropic requires tool_use to be the LAST content
					// block whenever stop_reason ends up "tool_use" —
					// drop trailing text instead of appending it after an
					// already-emitted call.
					continue
				}
				appendOrCoalesceText(claudeResp, t)
				cr.textChars += len(t)
				cr.lastText += t

			case part.Get("functionCall").Exists():
				fc := part.Get("functionCall")
				if p.SuppressToolCalls {
					logx.LogGateway("[Tools] tool calls are forbidden for this request: dropping %q", fc.Get("name").String())
					cr.droppedCalls++
					continue
				}
				if len(cr.toolsCalled) > 0 && wantsSingleToolUse(claudeBodyBytes) {
					logx.LogGateway("[Tools] disable_parallel_tool_use: dropping extra tool call %q (only the first call of a turn is kept)", fc.Get("name").String())
					continue
				}
				fname := fc.Get("name").String()
				if orig, ok := p.ReverseToolNameMap[fname]; ok {
					fname = orig
				}
				argsRaw := fc.Get("args").Raw
				if argsRaw == "" || argsRaw == "null" {
					argsRaw = "{}"
				}
				argsRaw = toolargs.NormalizeToArgsObject(fname, argsRaw)
				argsRaw = schema.CoerceNumericEnumArgs(p.SessionID, fname, argsRaw)
				if repaired, ok := editrepair.RepairEditToolCall(fname, argsRaw, cwdKey); ok {
					argsRaw = repaired
				}
				if widened, ok := loopguard.Global().WidenPagedRead(p.AgentKey+"|"+p.SessionID+"|"+p.InstanceID, fname, argsRaw); ok {
					logx.LogGateway("[LoopGuard] widened paged Read to %d lines for %s", loopguard.WidenedReadLimit, p.AgentKey)
					argsRaw = widened
				}
				switch strings.ToLower(fname) {
				case "grep":
					if repaired, ok := editrepair.RepairGrepToolCall(argsRaw); ok {
						argsRaw = repaired
					}
				case "glob":
					if repaired, ok := editrepair.RepairGlobToolCall(argsRaw); ok {
						argsRaw = repaired
					}
				}
				if p.SyntheticReasonTools[fname] {
					if cleaned, err := sjson.Delete(argsRaw, "reason"); err == nil {
						argsRaw = cleaned
					}
				}
				argsRaw = schema.StripNestedOpenDictReason(p.SessionID, fname, argsRaw)

				// toolargs.NormalizeToArgsObject already guarantees
				// argsRaw decodes to a JSON object, so this unmarshal
				// always succeeds into a map — no separate bare-string/
				// non-object fallback needed here, unlike v1's own
				// hand-rolled duplicate of that same guarantee.
				var argsObj interface{}
				_ = json.Unmarshal([]byte(argsRaw), &argsObj)
				if argsObj == nil {
					argsObj = map[string]interface{}{}
				}

				cr.toolsCalled = append(cr.toolsCalled, fname)
				cr.toolCallSignatures = append(cr.toolCallSignatures, fmt.Sprintf("%s:%s", fname, argsRaw))
				callID := toolcallid.Next(fname)
				cr.toolCallIDs = append(cr.toolCallIDs, callID)
				callSig := partSig
				if callSig == "" {
					callSig = candidateThoughtSig
				}
				if callSig == "" {
					callSig = lastTurnThoughtSig
				}
				if callSig == "" {
					callSig = toolsig.GetSessionSignature(cwdKey, resolvedModel)
				}
				toolsig.Set(callID, callSig, resolvedModel)
				claudeResp["content"] = append(claudeResp["content"].([]map[string]interface{}), map[string]interface{}{
					"type":  "tool_use",
					"id":    callID,
					"name":  fname,
					"input": argsObj,
				})
				if claudeResp["stop_reason"] != "max_tokens" && claudeResp["stop_reason"] != "refusal" {
					claudeResp["stop_reason"] = "tool_use"
				}
			}
		}
	}

	// No sources footer after a refusal or a max_tokens cut-off (mirrors the
	// streaming path): it would append prose to a truncated/blocked answer.
	if gm := cand.Get("groundingMetadata"); claudeResp["stop_reason"] != "tool_use" && gm.Exists() && groundingFooterAllowed(cand) {
		if chunks := gm.Get("groundingChunks"); chunks.IsArray() && len(chunks.Array()) > 0 {
			var sb strings.Builder
			if langdetect.IsRussianConversation(req.Messages) {
				sb.WriteString("\n\n---\n**Источники Google Search:**\n")
			} else {
				sb.WriteString("\n\n---\n**Google Search Sources:**\n")
			}
			seen := make(map[string]bool)
			for _, c := range chunks.Array() {
				uri := c.Get("web.uri").String()
				title := c.Get("web.title").String()
				if uri != "" && !seen[uri] {
					seen[uri] = true
					if title == "" {
						title = uri
					}
					fmt.Fprintf(&sb, "- [%s](%s)\n", title, uri)
				}
			}
			cr.toolsCalled = append(cr.toolsCalled, "googleSearch")
			appendOrCoalesceText(claudeResp, sb.String())
		}
	}

	return cr
}

// groundingFooterAllowed reports whether the finish reason permits the
// sources footer: never after a refusal or a max_tokens cut-off.
func groundingFooterAllowed(cand gjson.Result) bool {
	switch stream.ClassifyFinishReason(cand.Get("finishReason").String()).StopReason {
	case "refusal", "max_tokens":
		return false
	}
	return true
}

// appendOrCoalesceText appends text to the last content block if it is already
// a text block (Anthropic specification strictly mandates coalescing adjacent text
// fragments), or appends a new text block if content is empty or the last block
// is a thinking or tool block. It also ensures loopguard escape tags are stripped
// so they never leak into client output or history.
func appendOrCoalesceText(claudeResp map[string]interface{}, text string) {
	if loopguard.HasIgnoreLoopTag(text) {
		text = loopguard.StripIgnoreLoopTag(text)
	}
	if text == "" {
		return
	}
	blocks, ok := claudeResp["content"].([]map[string]interface{})
	if !ok {
		claudeResp["content"] = []map[string]interface{}{{
			"type": "text",
			"text": text,
		}}
		return
	}
	if len(blocks) > 0 {
		last := blocks[len(blocks)-1]
		if last["type"] == "text" {
			last["text"] = last["text"].(string) + text
			return
		}
	}
	claudeResp["content"] = append(blocks, map[string]interface{}{
		"type": "text",
		"text": text,
	})
}

// appendThinkingBlock folds Gemini's "thought" parts into AT MOST one
// Anthropic "thinking" content block (Anthropic strictly mandates at
// most one per message, first in content) — Gemini can split one
// thinking chain across several adjacent "thought" parts in the same
// candidate.
func appendThinkingBlock(claudeResp map[string]interface{}, text, sig string) {
	contentBlocks := claudeResp["content"].([]map[string]interface{})
	var existing map[string]interface{}
	for _, b := range contentBlocks {
		if b["type"] == "thinking" {
			existing = b
			break
		}
	}
	if existing != nil {
		if text != "" {
			existing["thinking"] = existing["thinking"].(string) + text
		}
		if sig != "" && (existing["signature"] == "" || existing["signature"] == "skip_thought_signature_validator") {
			existing["signature"] = sig
		}
		return
	}
	if text == "" && sig == "" {
		return
	}
	if sig == "" {
		sig = "skip_thought_signature_validator"
	}
	block := map[string]interface{}{
		"type":      "thinking",
		"thinking":  text,
		"signature": sig,
	}
	claudeResp["content"] = append([]map[string]interface{}{block}, contentBlocks...)
}
