// Package toolcallid generates Anthropic-shaped tool_use ids
// ("toolu_<name>_<nanots>_<seq>"). Extracted out of superrelay (v1)'s
// sse.go (nextToolCallID) into its own leaf package rather than left
// inside internal/stream: internal/stream needs it when a NEWLY
// generated tool call streams out (HandleFunctionCall), but
// internal/orchestrate's non-streaming path independently needs the
// identical id format when building its own tool_use blocks
// (executeNonStreaming in v1 called the same function directly, since v1
// is one `package main`). Same single-ownership rationale as
// internal/toolsig/internal/toolargs in this same migration phase.
package toolcallid

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// seq makes generated tool_use ids collision-free regardless of timer
// resolution: time.Now().UnixNano() alone can return the same value for
// two tool calls processed microseconds apart (common on Windows/VMs
// with coarse clock granularity).
var seq atomic.Uint64

// Next builds a tool_use id of the form "toolu_<name>_<nanots>_<seq>".
// Anthropic's real tool_use.id pattern is ^[a-zA-Z0-9_-]{1,64}$ — a long
// MCP tool name plus a decimal nanosecond timestamp routinely exceeded 64
// characters, which a strict client SDK rejects outright. Hex-encoding
// the numeric parts buys back room; the name itself is truncated as a
// last resort only if it still doesn't fit.
func Next(name string) string {
	safeName := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return '_'
	}, name)
	nanoHex := fmt.Sprintf("%x", time.Now().UnixNano())
	seqHex := fmt.Sprintf("%x", seq.Add(1))

	const prefix = "toolu_"
	fixedLen := len(prefix) + len(nanoHex) + len(seqHex) + 2 // 2 separators
	maxNameLen := 64 - fixedLen
	if maxNameLen < 1 {
		maxNameLen = 1
	}
	if len(safeName) > maxNameLen {
		safeName = safeName[:maxNameLen]
	}
	id := fmt.Sprintf("%s%s_%s_%s", prefix, safeName, nanoHex, seqHex)
	if len(id) > 64 {
		id = id[:64]
	}
	return id
}
