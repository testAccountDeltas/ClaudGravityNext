// Part of the stream package — see events.go for the package doc
// comment. Split out of fsm.go (GAP-P2-05: that file had grown past the
// project's 400-line ceiling) — Stats is the one exported read-only
// snapshot seam, not FSM's own state machine logic, so it belongs in its
// own file rather than padding fsm.go further.
package stream

// Stats is the small subset of FSM's own bookkeeping fields that
// internal/orchestrate needs once a stream finishes, to feed LoopGuard
// (RecordReadTurnWithRealProgress/RecordToolCalls) and the request log —
// v1's server.go read streamer.toolCallSignatures/textChars/lastText/
// toolNames/inputTokens/outputTokens/cacheTokens directly off
// *SSEStreamer, which this package's FSM keeps private; this is the one
// exported seam instead of exposing every field.
type Stats struct {
	ToolNames          []string
	ToolCallSignatures []string
	ToolCallIDs        []string
	TextChars          int
	ThinkingChars      int // GAP-TH-11
	LastText           string
	LastThinking       string // GAP-R17-30: thinking text for oscillation detection
	InputTokens        int64
	OutputTokens       int64
	CacheTokens        int64
}

// Stats snapshots f's bookkeeping fields for the caller to read once
// streaming has finished (StreamGeminiToClaude returned).
func (f *FSM) Stats() Stats {
	return Stats{
		ToolNames:          f.toolNames,
		ToolCallSignatures: f.toolCallSignatures,
		ToolCallIDs:        f.toolCallIDs,
		TextChars:          f.textChars,
		ThinkingChars:      f.thinkingChars,
		LastText:           f.lastText,
		LastThinking:       f.lastThinking,
		InputTokens:        f.inputTokens,
		OutputTokens:       f.outputTokens,
		CacheTokens:        f.cacheTokens,
	}
}
