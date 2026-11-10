// Part of the stream package — see events.go for the package doc
// comment. Replaces v1's two ad-hoc "map + sync.Mutex + its own
// sweepStale* goroutine, started from init()" caches (toolSignatureMap/
// toolSignatureSeen and sessionCacheFractionMap/sessionCacheFractionSeen)
// with internal/ephemeral.Store — exactly the duplication that package's
// own doc comment names these two v1 caches as examples of.
//
// The tool-signature half of this now lives in internal/toolsig, not
// here: internal/translate needs read access to the exact same cache
// (replaying a tool_use/thinking block on a later turn) and must not
// depend on this whole package to get it. setToolSignature/
// getToolSignature below just delegate.
package stream

import (
	"strings"
	"time"

	"claudgravity/internal/ephemeral"
	"claudgravity/internal/toolsig"
)

// cacheCreationThreshold is the minimum uncached-input size Google's real
// model actually creates a cache entry for — Haiku's floor (2048) is
// higher than every other model's (1024).
func cacheCreationThreshold(claudeModelName string) int64 {
	if strings.Contains(strings.ToLower(claudeModelName), "haiku") {
		return 2048
	}
	return 1024
}

const (
	toolSignatureStaleAfter = 2 * time.Hour
	toolSignatureSweepEvery = 30 * time.Minute
)

func setToolSignature(callID, signature, model string) { toolsig.Set(callID, signature, model) }

func getToolSignature(callID, model string) string { return toolsig.Get(callID, model) }

// sessionCacheFraction remembers, per session, the last REAL
// cache_read/input_tokens ratio this conversation saw at the end of a
// turn. message_start has to guess its usage numbers before Gemini has
// even started generating — Google's real cache split only arrives in
// the stream's last chunk — so it always sent cache_read_input_tokens:0
// there. Official Anthropic SDKs latch input_tokens/
// cache_read_input_tokens from message_start only and never update them
// from message_delta's real values, so every client built on one of
// those SDKs displayed a permanent 0% cache hit rate regardless of the
// real (correct) numbers later turns' message_delta carried. Guessing
// from the PREVIOUS turn's real ratio is the best available estimate for
// a conversation that's actively warm — still corrected to the real
// number in message_delta for anything that reads that instead.
var sessionCacheFraction = ephemeral.New[float64](toolSignatureStaleAfter, toolSignatureSweepEvery)

func setSessionCacheFraction(sessionID string, frac float64) {
	if sessionID == "" {
		return
	}
	sessionCacheFraction.Set(sessionID, frac)
}

func getSessionCacheFraction(sessionID string) (float64, bool) {
	return sessionCacheFraction.Get(sessionID)
}

// SetSessionCacheFraction exports setSessionCacheFraction for
// internal/orchestrate's non-streaming path, which needs to record a
// turn's real cache_read/input_tokens ratio from OUTSIDE an FSM (the
// streaming path's own FinishStream already calls the private setter
// internally as part of finishing a stream).
func SetSessionCacheFraction(sessionID string, frac float64) {
	setSessionCacheFraction(sessionID, frac)
}

func (f *FSM) updatePendingThinkingSignature(sig string) {
	if sig == "" {
		return
	}
	toolsig.SetSessionSignature(f.cwdKey, sig, f.resolvedModel)
	f.lastTurnThoughtSignature = sig
	if f.currentBlock == BlockThinking && f.thoughtSignature == "" {
		f.thoughtSignature = sig
	}
}

func (f *FSM) flushPendingThinkingSignatureFallback() {
	// signature_delta is emitted strictly before content_block_stop in closeCurrentBlock.
	// This function remains a safe no-op for FinishStream and FinishWithError call sites.
}
