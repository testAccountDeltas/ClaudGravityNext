// Package toolsig remembers the real Gemini thoughtSignature attached to
// a functionCall part, keyed by that call's own tool_use id. Anthropic's
// tool_use content block has no signature field to round-trip it through
// Claude Code itself (unlike "thinking" blocks, which do) — this is the
// only way to recover the real signature when that same tool_use id is
// echoed back on a later turn, instead of always falling back to
// Google's "skip_thought_signature_validator" bypass, which its own docs
// warn degrades reasoning quality across multi-turn tool-calling chains.
//
// Extracted out of superrelay (v1)'s sse.go (toolSignatureMap) into its
// own leaf package rather than left inside internal/stream: the WRITE
// side happens in internal/stream (a tool call is first emitted while
// streaming a response), but the READ side is needed by internal/translate
// (replaying that same tool_use on a later turn's history, and recovering
// a thinking block's real signature from a sibling tool_use) — translate
// must never depend on the whole stream package just for this, and stream
// must not depend on translate, so the cache itself needs to be the leaf
// both sides import.
package toolsig

import (
	"strings"
	"time"

	"claudgravity/internal/ephemeral"
)

const (
	staleAfter = 2 * time.Hour
	sweepEvery = 30 * time.Minute
)

var signatures = ephemeral.New[string](staleAfter, sweepEvery)

// Set records signature as the real thoughtSignature for callID.
func Set(callID, signature string) {
	if callID == "" || signature == "" {
		return
	}
	signatures.Set(callID, signature)
}

// Get returns the real signature recorded for callID, or "" if none is
// cached (expired or never set).
func Get(callID string) string {
	sig, _ := signatures.Get(callID)
	return sig
}

// GAP-TH-06: the callID-keyed cache above has nothing to key under for
// a PURE-TEXT turn (no tool call at all) — if that turn's real
// signature arrives split across SSE chunks such that it lands after
// the thinking block already closed, there is no tool_use id to
// recover it from on the next turn, and translate.go permanently falls
// back to the skip-validator placeholder for that session. A second,
// session-keyed "last real signature seen" cache gives convertThinkingBlock
// one more fallback before giving up.
var sessionSignatures = ephemeral.New[string](staleAfter, sweepEvery)

// SetSessionSignature records signature as the most recent real
// thoughtSignature seen anywhere in sessionID's stream.
func SetSessionSignature(sessionID, signature string) {
	if sessionID == "" || signature == "" || signature == "skip_thought_signature_validator" {
		return
	}
	sessionSignatures.Set(sessionID, signature)
}

// GetSessionSignature returns the most recent real signature recorded
// for sessionID, or "" if none is cached. If sessionID is a compound key
// (e.g. sessionID:instanceID for a subagent), it falls back to the parent
// sessionID if no subagent-specific signature is cached yet. Supports arbitrary
// colon-delimited hierarchies (e.g. tenant:sessionID:subagentID) by peeling
// off segments from the right until a parent with a cached signature is found.
func GetSessionSignature(sessionID string) string {
	if sig, ok := sessionSignatures.Get(sessionID); ok && sig != "" {
		return sig
	}
	curr := sessionID
	for {
		idx := strings.LastIndex(curr, ":")
		if idx == -1 {
			break
		}
		curr = curr[:idx]
		if sig, ok := sessionSignatures.Get(curr); ok && sig != "" {
			return sig
		}
	}
	return ""
}
