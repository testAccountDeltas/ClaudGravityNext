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
//
// Two gaps found by comparing against 9router's equivalent
// (thoughtSignatureStore.js), both fixed here:
//
//  1. Family isolation. Antigravity serves both Gemini and native Claude
//     (via server.model_overrides / the quota-exhaustion remap in
//     orchestrate's remapExhaustedNative3PModel) behind the same endpoint,
//     and each backend only accepts signatures IT produced — replaying a
//     Claude-produced signature into a Gemini request (or vice versa)
//     fails with 400 "Corrupted thought signature." A session that
//     crosses that boundary mid-conversation (quota exhausted, falls back
//     from claude-sonnet-4-6 to gemini-3.8-flash-tiered) used to risk
//     exactly that. Every entry now carries the family ("claude"/"gemini"/
//     the raw model string for anything else) it was produced under, and
//     a lookup for a different family is treated as a cache miss — same
//     fallback behavior as "never cached", not an error.
//  2. Restart survival. This cache was pure in-process RAM — a deploy,
//     crash, or VPS reboot mid-conversation silently dropped every cached
//     signature for every active session, guaranteeing the exact cache
//     collapse this comment used to only warn about ("the gateway
//     restarted" was already listed in toolcall.go as one of the three
//     causes of a lost signature). persist.go adds an async SQLite
//     fallback (same db file as internal/requestlog) checked on a RAM
//     miss and written on every Set — so an in-flight conversation
//     recovers its signatures after a restart instead of starting the
//     whole cache from zero.
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

// sigEntry is what both the RAM store and the persisted fallback hold:
// the signature itself plus the family it's only valid for.
type sigEntry struct {
	Signature string
	Family    string
}

var signatures = ephemeral.New[sigEntry](staleAfter, sweepEvery)

// Family returns the signature family a model belongs to: "claude" or
// "gemini" for the two backends Antigravity actually serves, or the
// lowercased model string itself for anything else (native GPT passthrough,
// an unrecognized future family) so two different "other" models still
// never match each other. An empty model name returns "" — the wildcard
// family that matches (and is matched by) anything, for callers that
// don't know or don't care which backend produced/will consume the entry.
func Family(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}
	switch {
	case strings.Contains(m, "claude"):
		return "claude"
	case strings.Contains(m, "gemini"):
		return "gemini"
	default:
		return m
	}
}

// compatible reports whether an entry recorded under recordedFamily may be
// replayed for a lookup under wantFamily. Either side being "" (unknown —
// an old entry predating this field, or a caller that didn't pass a model)
// matches anything, the same rule 9router's isCompatible uses.
func compatible(recordedFamily, wantFamily string) bool {
	return recordedFamily == "" || wantFamily == "" || recordedFamily == wantFamily
}

// Set records signature as the real thoughtSignature for callID, tagged
// with the family of model (the model that actually produced it — pass ""
// when unknown, which makes the entry match any family on lookup).
func Set(callID, signature, model string) {
	if callID == "" || signature == "" {
		return
	}
	fam := Family(model)
	signatures.Set(callID, sigEntry{Signature: signature, Family: fam})
	persistSet(callID, signature, fam)
}

// Get returns the real signature recorded for callID, or "" if none is
// cached (expired, never set, or recorded under an incompatible family for
// model). model is the model this signature would be replayed INTO; pass ""
// to accept a signature from any family (the pre-family-guard behavior).
func Get(callID, model string) string {
	if callID == "" {
		return ""
	}
	wantFam := Family(model)
	if e, ok := signatures.Get(callID); ok {
		if compatible(e.Family, wantFam) {
			return e.Signature
		}
		return ""
	}
	if sig, fam, ok := persistGet(callID); ok {
		if !compatible(fam, wantFam) {
			return ""
		}
		// Warm the RAM cache so the next lookup (almost always within the
		// same streaming turn or the next one) doesn't hit SQLite again.
		signatures.Set(callID, sigEntry{Signature: sig, Family: fam})
		return sig
	}
	return ""
}

// GAP-TH-06: the callID-keyed cache above has nothing to key under for
// a PURE-TEXT turn (no tool call at all) — if that turn's real
// signature arrives split across SSE chunks such that it lands after
// the thinking block already closed, there is no tool_use id to
// recover it from on the next turn, and translate.go permanently falls
// back to the skip-validator placeholder for that session. A second,
// session-keyed "last real signature seen" cache gives convertThinkingBlock
// one more fallback before giving up.
var sessionSignatures = ephemeral.New[sigEntry](staleAfter, sweepEvery)

// SetSessionSignature records signature as the most recent real
// thoughtSignature seen anywhere in sessionID's stream, tagged with the
// family of model (pass "" when unknown).
func SetSessionSignature(sessionID, signature, model string) {
	if sessionID == "" || signature == "" || signature == "skip_thought_signature_validator" {
		return
	}
	fam := Family(model)
	sessionSignatures.Set(sessionID, sigEntry{Signature: signature, Family: fam})
	persistSet("session:"+sessionID, signature, fam)
}

// GetSessionSignature returns the most recent real signature recorded
// for sessionID under a family compatible with model, or "" if none is
// cached. If sessionID is a compound key (e.g. sessionID:instanceID for a
// subagent), it falls back to the parent sessionID if no subagent-specific
// signature is cached yet. Supports arbitrary colon-delimited hierarchies
// (e.g. tenant:sessionID:subagentID) by peeling off segments from the
// right until a parent with a cached signature is found. model is the
// model this signature would be replayed INTO; pass "" to accept any family.
func GetSessionSignature(sessionID, model string) string {
	wantFam := Family(model)
	if sig := getSessionSignatureRAM(sessionID, wantFam); sig != "" {
		return sig
	}
	curr := sessionID
	for {
		idx := strings.LastIndex(curr, ":")
		if idx == -1 {
			break
		}
		curr = curr[:idx]
		if sig := getSessionSignatureRAM(curr, wantFam); sig != "" {
			return sig
		}
	}

	// RAM missed at every level (fresh process after a restart, or the
	// 1h-equivalent TTL lapsed) — fall back to the persisted store at the
	// exact sessionID first, then walk the same parent chain.
	if sig, fam, ok := persistGet("session:" + sessionID); ok && compatible(fam, wantFam) {
		sessionSignatures.Set(sessionID, sigEntry{Signature: sig, Family: fam})
		return sig
	}
	curr = sessionID
	for {
		idx := strings.LastIndex(curr, ":")
		if idx == -1 {
			break
		}
		curr = curr[:idx]
		if sig, fam, ok := persistGet("session:" + curr); ok && compatible(fam, wantFam) {
			sessionSignatures.Set(curr, sigEntry{Signature: sig, Family: fam})
			return sig
		}
	}
	return ""
}

func getSessionSignatureRAM(sessionID, wantFam string) string {
	if e, ok := sessionSignatures.Get(sessionID); ok && e.Signature != "" && compatible(e.Family, wantFam) {
		return e.Signature
	}
	return ""
}
