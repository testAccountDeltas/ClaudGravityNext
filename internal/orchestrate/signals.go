// Part of the orchestrate package — see pipeline.go for the package doc
// comment. Small pure signal-extraction helpers ported from superrelay
// (v1)'s server.go, used by streaming.go/nonstreaming.go to decide
// cache-accounting/stop-reason/tool-use-limiting behavior for one
// attempt. Kept on raw bytes + gjson, like v1, rather than re-decoding
// into claude.Request a second time per helper call — these run on the
// hot path of every single attempt.
package orchestrate

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
)

func hasAssistantTurns(claudeBody []byte) bool {
	msgs := gjson.GetBytes(claudeBody, "messages")
	if !msgs.IsArray() {
		return false
	}
	for _, m := range msgs.Array() {
		if m.Get("role").String() == "assistant" {
			return true
		}
	}
	return false
}

// hasCacheControlBreakpoint reports whether the client's own request
// actually asked for prompt caching (an Anthropic "cache_control":
// {"type":"ephemeral"} block somewhere in system/tools/messages). A plain
// substring check is deliberately used instead of walking the full JSON
// structure: "cache_control" (with its quotes, a JSON-key shape) plus
// "ephemeral" together are specific enough that a false positive is
// practically impossible, and this runs on every request.
func hasCacheControlBreakpoint(claudeBody []byte) bool {
	return bytes.Contains(claudeBody, []byte(`"cache_control"`)) && bytes.Contains(claudeBody, []byte(`"ephemeral"`))
}

// cacheCreationThreshold returns the minimum uncached-prompt size
// (tokens) below which the cache_creation_input_tokens simulation
// doesn't fire. Anthropic's own spec sets a higher minimum cacheable
// prefix, 2048 tokens, specifically for the Haiku model family.
func cacheCreationThreshold(claudeModelName string) int64 {
	if strings.Contains(strings.ToLower(claudeModelName), "haiku") {
		return 2048
	}
	return 1024
}

// cacheBreakpointPrefixFraction approximates how much of the request's
// uncached input actually sits BEFORE the last cache_control breakpoint
// (the part that would really get written to cache) versus after it
// (the current turn's own new content, which real Anthropic billing
// still counts as plain input_tokens). There's no real token-level
// breakpoint signal available (this whole thing is a simulation — Gemini
// never reports it), so this uses the breakpoint's byte offset within the
// request body as a proxy for its proportion of the token stream —
// coarse, but far closer than treating it as 100% either way.
func cacheBreakpointPrefixFraction(claudeBody []byte) float64 {
	idx := bytes.LastIndex(claudeBody, []byte(`"cache_control"`))
	if idx < 0 || len(claudeBody) == 0 {
		return 0.0
	}
	frac := float64(idx) / float64(len(claudeBody))
	if frac < 0 {
		return 0
	}
	if frac > 1 {
		return 1
	}
	return frac
}

// wantsSingleToolUse reports whether the client set
// disable_parallel_tool_use — the official Anthropic SDKs send this
// nested inside tool_choice, not as a top-level field.
func wantsSingleToolUse(claudeBody []byte) bool {
	return gjson.GetBytes(claudeBody, "disable_parallel_tool_use").Bool() || gjson.GetBytes(claudeBody, "tool_choice.disable_parallel_tool_use").Bool()
}

// stripThoughtFieldsForPublicAPI removes {"thought":true,...} parts'
// thought/thoughtSignature keys before a request goes to the public
// Gemini Developer API, which documents "thought" as output-only on
// input contents and 400s with "Field 'thought' is not supported for
// input contents" if a replayed conversation still carries it
// (internal/translate always emits it regardless of which upstream the
// request will hit).
func stripThoughtFieldsForPublicAPI(geminiBody []byte) []byte {
	if !gjson.GetBytes(geminiBody, "contents").Exists() {
		return geminiBody
	}
	var root map[string]interface{}
	if err := json.Unmarshal(geminiBody, &root); err != nil {
		return geminiBody
	}
	changed := false
	// internal/translate may have already baked in thinkingBudget: -1
	// (ModeAuto), which only the internal CloudCode/Antigravity endpoint
	// understands; the public API rejects it outright with 400
	// INVALID_ARGUMENT. Pin it to a fixed, broadly-supported budget
	// instead when present.
	if genCfg, ok := root["generationConfig"].(map[string]interface{}); ok {
		if thinkCfg, ok := genCfg["thinkingConfig"].(map[string]interface{}); ok {
			if budget, ok := thinkCfg["thinkingBudget"].(float64); ok && budget < 0 {
				thinkCfg["thinkingBudget"] = 8192
				changed = true
			}
		}
	}
	contents, ok := root["contents"].([]interface{})
	if !ok {
		if changed {
			if out, err := json.Marshal(root); err == nil {
				return out
			}
		}
		return geminiBody
	}
	for _, c := range contents {
		cm, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		parts, ok := cm["parts"].([]interface{})
		if !ok {
			continue
		}
		for _, p := range parts {
			pm, ok := p.(map[string]interface{})
			if !ok {
				continue
			}
			if _, hasThought := pm["thought"]; hasThought {
				delete(pm, "thought")
				delete(pm, "thoughtSignature")
				changed = true
			} else if _, hasSig := pm["thoughtSignature"]; hasSig {
				// A functionCall part carries its own thoughtSignature too
				// (internal/translate sets one on every replayed
				// tool_use, to preserve call identity) — it has no
				// "thought" key at all, so the check above misses it,
				// and thoughtSignature is output-only on the public API
				// the exact same way "thought" itself is.
				delete(pm, "thoughtSignature")
				changed = true
			}
		}
	}
	if !changed {
		return geminiBody
	}
	out, err := json.Marshal(root)
	if err != nil {
		return geminiBody
	}
	return out
}
