// Part of the stream package — see events.go for the package doc comment.
// Gemini finishReason classification shared by the streaming FSM
// (finish.go) and the non-streaming path (internal/orchestrate): one table
// decides the Anthropic stop_reason AND whether the turn is a usable
// answer at all. Before this existed every reason except MAX_TOKENS and a
// safety set collapsed into a clean "end_turn", so a MALFORMED_FUNCTION_CALL
// or an empty turn reached the client as a successful, empty answer.
package stream

import (
	"fmt"
	"net/http"
)

// FinishClass is what one Gemini finishReason means for the Anthropic
// response.
type FinishClass struct {
	// StopReason is "end_turn", "max_tokens" or "refusal" (tool_use and
	// stop_sequence are decided later from what the turn actually did).
	StopReason string
	// ToolFailure marks reasons that mean the model TRIED to call a tool and
	// the call was lost (malformed / unexpected / too many / unsigned): the
	// turn is a failure unless a tool_use block was still delivered.
	ToolFailure bool
	// ErrType is the Anthropic error type reported when the turn is a
	// failure.
	ErrType string
}

// ClassifyFinishReason maps a Gemini finishReason ("" means "none seen").
func ClassifyFinishReason(fr string) FinishClass {
	switch fr {
	case "", "STOP", "FINISH_REASON_UNSPECIFIED":
		return FinishClass{StopReason: "end_turn", ErrType: "overloaded_error"}
	case "MAX_TOKENS", "CONTINUATION":
		// CONTINUATION is not in Google's public enum but is documented as
		// "output was cut off, continue" — the same meaning as max_tokens.
		return FinishClass{StopReason: "max_tokens", ErrType: "overloaded_error"}
	case "SAFETY", "PROHIBITED_CONTENT", "BLOCKLIST", "SPII", "MALICIOUS",
		"IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT":
		return FinishClass{StopReason: "refusal", ErrType: "api_error"}
	case "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "TOO_MANY_TOOL_CALLS",
		"MISSING_THOUGHT_SIGNATURE":
		return FinishClass{StopReason: "end_turn", ToolFailure: true, ErrType: "api_error"}
	case "MALFORMED_RESPONSE":
		return FinishClass{StopReason: "end_turn", ErrType: "api_error"}
	default:
		// RECITATION, LANGUAGE, OTHER, NO_IMAGE and anything Google adds
		// later. RECITATION/LANGUAGE are NOT safety refusals (the old
		// mapping reported them as "general_harms" refusals): whatever text
		// came out is delivered as a normal end; only a turn with nothing
		// usable in it is a failure worth retrying.
		return FinishClass{StopReason: "end_turn", ErrType: "overloaded_error"}
	}
}

// UpstreamTurnError is a turn that must not be reported as a success: an
// abnormal finishReason, an empty turn, a stream cut off before any
// finishReason, or an in-band upstream error. The orchestrator retries it
// (preferring a different account) while nothing has reached the client.
type UpstreamTurnError struct {
	ErrType      string // Anthropic error type
	Status       int    // HTTP status the retry/final-error path should use
	FinishReason string
	Msg          string
	// InBand marks a Google error chunk (429/503/...) as opposed to a bad
	// MODEL turn: the former is a verdict on the account (cooldown), the
	// latter is not.
	InBand bool
	// NeedsMoreOutput marks a MAX_TOKENS turn with no visible output whose
	// hidden thinking consumed the whole allowance: retried once with a
	// larger maxOutputTokens (orchestrate) instead of failing over.
	NeedsMoreOutput bool
}

func (e *UpstreamTurnError) Error() string {
	if e.FinishReason != "" {
		return fmt.Sprintf("%s (finishReason=%s)", e.Msg, e.FinishReason)
	}
	return e.Msg
}

func statusForErrType(errType string) int {
	if errType == "api_error" {
		return http.StatusInternalServerError
	}
	return http.StatusServiceUnavailable
}

// CheckTurn reports why the turn is not a usable answer, or nil. hasText /
// hasToolUse describe what the model actually produced (thinking alone does
// not count as an answer).
func (c FinishClass) CheckTurn(finishReason string, hasText, hasToolUse bool) *UpstreamTurnError {
	if c.StopReason == "refusal" || c.StopReason == "max_tokens" {
		return nil
	}
	switch {
	case c.ToolFailure && !hasToolUse:
		return &UpstreamTurnError{ErrType: c.ErrType, Status: statusForErrType(c.ErrType), FinishReason: finishReason,
			Msg: "upstream model produced an unusable tool call"}
	case !hasText && !hasToolUse:
		return &UpstreamTurnError{ErrType: c.ErrType, Status: statusForErrType(c.ErrType), FinishReason: finishReason,
			Msg: "upstream model returned an empty response"}
	}
	return nil
}

// TruncatedTurnError is the stream that ended (EOF / dropped connection)
// without ever delivering a finishReason: a cut-off, never a clean end.
func TruncatedTurnError() *UpstreamTurnError {
	return &UpstreamTurnError{ErrType: "overloaded_error", Status: http.StatusServiceUnavailable,
		Msg: "upstream stream ended without a finishReason (truncated response)"}
}
