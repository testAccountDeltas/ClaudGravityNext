// Part of the claude protocol package — see message.go for the package
// doc comment.
package claude

// StopReason is Claude's enumerated reason a response ended — grounded
// in the exact strings sse.go/server.go map Gemini's finishReason onto.
type StopReason string

const (
	StopEndTurn      StopReason = "end_turn"
	StopMaxTokens    StopReason = "max_tokens"
	StopToolUse      StopReason = "tool_use"
	StopStopSequence StopReason = "stop_sequence"
	StopRefusal      StopReason = "refusal"
	StopPauseTurn    StopReason = "pause_turn"
)

// Usage mirrors the Anthropic response's usage object exactly —
// superrelay (v1)'s token-display fix (see superrelay-token-display-fix
// in the maintainer's own notes) depends on InputTokens specifically
// being the number message_delta reports, not OutputTokens-only.
type Usage struct {
	InputTokens              int64                `json:"input_tokens"`
	OutputTokens             int64                `json:"output_tokens"`
	CacheCreationInputTokens int64                `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64                `json:"cache_read_input_tokens,omitempty"`
	OutputTokensDetails      *OutputTokensDetails `json:"output_tokens_details,omitempty"`
}

// OutputTokensDetails breaks OutputTokens down by kind — GAP-TH-07: this
// was missing from the typed domain entirely, even though it's part of
// Anthropic's documented usage shape when extended thinking is active.
type OutputTokensDetails struct {
	ThinkingTokens int64 `json:"thinking_tokens"`
}

// StopDetails describes the reason a generation stopped in detail.
type StopDetails struct {
	Type        string `json:"type"`
	Category    string `json:"category,omitempty"`
	Explanation string `json:"explanation,omitempty"`
}

// Response is a non-streaming POST /v1/messages response.
type Response struct {
	ID           string       `json:"id"`
	Type         string       `json:"type"` // "message"
	Role         Role         `json:"role"`
	Model        string       `json:"model"`
	Content      []Block      `json:"content"`
	StopReason   StopReason   `json:"stop_reason"`
	StopSequence *string      `json:"stop_sequence"`
	StopDetails  *StopDetails `json:"stop_details"`
	Usage        Usage        `json:"usage"`
}
