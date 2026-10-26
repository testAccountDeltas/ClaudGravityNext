// Part of the tokens package — see text.go for the package doc comment.
// WeightedRequestTokens ports v1's estimateApproxClaudeTokens
// (token_estimator.go) — the "realistic" estimate this gateway reports to
// the client (message_start's input_tokens, count_tokens).
package tokens

import (
	"encoding/json"
	"strings"

	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/toolschema"
)

// WeightedRequestTokens estimates the full Anthropic Messages API
// request's token count (system, messages, tool definitions) using
// WeightedCharTokens for every text leaf.
func WeightedRequestTokens(req claude.Request) int {
	return WeightedRequestTokensFor(req, defaultProfile)
}

// WeightedRequestTokensFor is WeightedRequestTokens under an explicit
// model-family Profile (see ProfileForModel).
func WeightedRequestTokensFor(req claude.Request, prof Profile) int {
	total := 0

	total += weightedSystemTokens(req.System)
	total += weightedMessagesTokens(req.Messages, prof)
	total += weightedToolsTokens(req.Tools)

	// Calibration from the live drift log (estimate / Gemini's promptTokenCount
	// over 7617 requests): median 0.989, p5 0.873, p95 1.149, i.e. about half of
	// the requests were estimated BELOW what Gemini counted, while the target
	// band is [1.00, 1.30]. One multiplier moves the median to about 1.03 and
	// leaves the upper tail near 1.20.
	total = int(float64(total) * estimateCalibration)

	if total < 1 {
		return 1
	}
	return total
}

// estimateCalibration scales the weighted estimate; see WeightedRequestTokensFor.
const estimateCalibration = 1.04

func weightedSystemTokens(sys claude.SystemPrompt) int {
	if len(sys) == 0 {
		return 0
	}
	// Mirrors Request.SystemText's own two-shape decode, but prices each
	// block separately rather than joining them first — matches v1's own
	// per-block loop exactly (no inter-block "\n" join contributes extra
	// weighted characters that were never actually there).
	total := 0
	var s string
	if err := json.Unmarshal(sys, &s); err == nil {
		return WeightedCharTokens(s)
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(sys, &blocks); err == nil {
		for _, b := range blocks {
			total += WeightedCharTokens(b.Text)
		}
		return total
	}
	var strs []string
	if err := json.Unmarshal(sys, &strs); err == nil {
		for _, str := range strs {
			total += WeightedCharTokens(str)
		}
	}
	return total
}

func weightedMessagesTokens(msgs []claude.Message, prof Profile) int {
	if len(msgs) == 0 {
		return 0
	}
	lastAssistantIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == claude.RoleAssistant {
			lastAssistantIdx = i
			break
		}
	}

	total := 0
	for i, m := range msgs {
		isLastAssistant := m.Role == claude.RoleAssistant && i == lastAssistantIdx
		total += 3 // per-message framing overhead
		for _, b := range m.Content {
			total += blockCostP(b, isLastAssistant, WeightedCharTokens, prof)
		}
	}
	return total
}

func weightedToolsTokens(tools []claude.Tool) int {
	total := 0
	for _, t := range tools {
		total += WeightedCharTokens(t.Name)
		total += WeightedCharTokens(t.Description)
		rawSchema := strings.TrimSpace(string(t.InputSchema))
		if rawSchema == "" || rawSchema == "null" || rawSchema == "{}" {
			// Anthropic's server-side built-in tools (bash_20250124,
			// text_editor_20250728, computer_20250124, ...) are declared
			// by the client with NO input_schema at all — Anthropic's own
			// server fills in and bills for a real parameter schema
			// underneath, so price the reference schema instead of 0.
			if builtin, ok := toolschema.Builtin(t.Type); ok {
				rawSchema = builtin
			}
		}
		total += WeightedCharTokens(rawSchema)
	}
	return total
}
