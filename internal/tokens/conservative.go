// Part of the tokens package — see text.go for the package doc comment.
// Ports the "conservative" half of v1's context_window.go: a pessimistic,
// flat-rate estimate used ONLY by TruncateConversationToFit's safety
// check, where undercounting means a request slips through and hits a
// raw context-length 400 from Google — it must err on the side of
// over-truncating, never under-truncating.
package tokens

import (
	"encoding/json"

	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/toolschema"
)

// ConservativeMessageTokens prices one message's content blocks using the
// flat FlatTextTokens leaf, via the same merged blockCost dispatcher
// WeightedRequestTokens uses. v1's conservativeTokenEstimate had a
// fallback path for when a message's "content" wasn't a recognized list
// type at all — moot here, since claude.Message's own decode always
// normalizes content to []claude.Block (see internal/protocol/claude's
// Message.UnmarshalJSON), so every message this package sees already has
// that shape.
func ConservativeMessageTokens(m claude.Message, isLastAssistant bool) int {
	return ConservativeMessageTokensFor(m, isLastAssistant, defaultProfile)
}

// ConservativeMessageTokensFor is ConservativeMessageTokens under an
// explicit model-family Profile.
func ConservativeMessageTokensFor(m claude.Message, isLastAssistant bool, prof Profile) int {
	total := 0
	for _, b := range m.Content {
		total += blockCostP(b, isLastAssistant, FlatTextTokens, prof)
	}
	return total
}

// ConservativeSystemTokens prices the raw System field's own bytes
// directly (whatever shape it arrived in) — matches v1's
// conservativeTokenEstimate(sys, false) call, which flat-estimates
// whatever untouched value reqMap["system"] held.
func ConservativeSystemTokens(sys claude.SystemPrompt) int {
	if len(sys) == 0 {
		return 0
	}
	return FlatJSONTokens(sys)
}

// ConservativeToolsTokens prices the tool definitions' own bytes directly
// — matches v1's conservativeTokenEstimate(tools, false) call.
func ConservativeToolsTokens(tools []claude.Tool) int {
	if len(tools) == 0 {
		return 0
	}
	// GAP-D4-06: a built-in tool (Type set, e.g. "bash_20250124") arrives
	// on the wire with no InputSchema at all — Anthropic's own server
	// fills that in — so marshaling it directly priced only the tiny
	// {"type":"...","name":"..."} stub (~20 tokens) instead of its real
	// declared parameter schema (~400 tokens), systematically
	// underestimating and letting a context-overflow condition slip
	// through this safety check.
	priced := make([]claude.Tool, len(tools))
	copy(priced, tools)
	for i, t := range priced {
		if len(t.InputSchema) == 0 && t.Type != "" {
			if schema, ok := toolschema.Builtin(t.Type); ok {
				priced[i].InputSchema = json.RawMessage(schema)
			}
		}
	}
	raw, err := json.Marshal(priced)
	if err != nil {
		return 0
	}
	return FlatJSONTokens(raw)
}

// ConservativeRequestTokens prices the full request's token count using
// conservative flat pricing for system, tools, and message blocks.
func ConservativeRequestTokens(req claude.Request) int {
	return ConservativeRequestTokensFor(req, defaultProfile)
}

// ConservativeRequestTokensFor is ConservativeRequestTokens under an
// explicit model-family Profile.
func ConservativeRequestTokensFor(req claude.Request, prof Profile) int {
	total := ConservativeSystemTokens(req.System) + ConservativeToolsTokens(req.Tools)
	lastAssistantIdx := -1
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == claude.RoleAssistant {
			lastAssistantIdx = i
			break
		}
	}
	for i, m := range req.Messages {
		total += ConservativeMessageTokensFor(m, i == lastAssistantIdx, prof) + 3 // framing overhead
	}
	return total
}
