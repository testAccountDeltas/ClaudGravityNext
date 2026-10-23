// Part of the orchestrate package — see pipeline.go for the package doc
// comment. Retry/failover policy: per-request constants, and the
// session-bump counter v1 kept as a package-level map+mutex+its own
// sweep goroutine (server.go's sessionBumpMap) — now backed by
// internal/ephemeral, same replacement already applied to internal/
// stream's tool-signature/cache-fraction caches.
package orchestrate

import (
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/ephemeral"
)

const (
	// maxAttempts bounds the OUTER account-rotation loop (a fresh account
	// selected from the pool each time).
	maxAttempts = 3
	// maxBumpsPerAttempt bounds ONE account attempt's own INNER retry
	// loop against the session token-wall — a local counter reset every
	// request, deliberately NOT checked against the session's cumulative
	// bump count (see getSessionBump's doc comment).
	maxBumpsPerAttempt = 3
	// maxLocationGatePasses bounds the INNER retry loop's host rotation —
	// full passes over warmup.MainInferenceHosts before giving up on this
	// account entirely.
	maxLocationGatePasses = 2
)

const (
	sessionBumpStaleAfter = 2 * time.Hour
	sessionBumpSweepEvery = 30 * time.Minute
)

// sessionBumps remembers, per session, the last session-id "bump" that
// got a long (>1M-token) conversation past the upstream session token
// wall. Without this, bump always restarted at 0 on every new HTTP
// request, so every turn after the wall was first hit paid for a
// guaranteed-failing bump=0 attempt before retrying with the bump that
// actually works.
var sessionBumps = ephemeral.New[int](sessionBumpStaleAfter, sessionBumpSweepEvery)

// getSessionBump reads back sessionID's last-known-good bump, refreshing
// its TTL on read as well as write — v1's own getSessionBump touched
// sessionBumpSeen on every read, keeping an actively-used (but rarely
// re-bumped) long session's entry alive indefinitely; ephemeral.Store's
// TTL only resets on Set, so this re-Sets the unchanged value to get the
// same "stays alive as long as it's used" behavior.
func getSessionBump(sessionID string) int {
	if sessionID == "" {
		return 0
	}
	bump, ok := sessionBumps.Get(sessionID)
	if !ok {
		return 0
	}
	sessionBumps.Set(sessionID, bump)
	return bump
}

// isThoughtSignatureMismatchError detects if a 400 Bad Request error from Google
// was caused by an invalid or mismatched thoughtSignature. Handles camelCase,
// snake_case, spaces, and case-insensitive variations from Google's protobuf/JSON errors.
func isThoughtSignatureMismatchError(status int, err error) bool {
	if status != 400 || err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "thoughtsignature") ||
		strings.Contains(lower, "thought signature") ||
		strings.Contains(lower, "thought_signature")
}

// replaceThoughtSignaturesWithSkipValidator traverses all parts in geminiBody
// and replaces any non-skip thoughtSignature with "skip_thought_signature_validator"
// to self-heal from deadlocked 400 signature validation errors.
func replaceThoughtSignaturesWithSkipValidator(geminiBody []byte) []byte {
	bodyStr := string(geminiBody)
	contents := gjson.GetBytes(geminiBody, "contents")
	if !contents.IsArray() {
		return geminiBody
	}
	contents.ForEach(func(cIdx, c gjson.Result) bool {
		if parts := c.Get("parts"); parts.IsArray() {
			parts.ForEach(func(pIdx, p gjson.Result) bool {
				if sig := p.Get("thoughtSignature").String(); sig != "" && sig != "skip_thought_signature_validator" {
					path := fmt.Sprintf("contents.%d.parts.%d.thoughtSignature", cIdx.Int(), pIdx.Int())
					bodyStr, _ = sjson.Set(bodyStr, path, "skip_thought_signature_validator")
				}
				if sig := p.Get("functionCall.thoughtSignature").String(); sig != "" && sig != "skip_thought_signature_validator" {
					path := fmt.Sprintf("contents.%d.parts.%d.functionCall.thoughtSignature", cIdx.Int(), pIdx.Int())
					bodyStr, _ = sjson.Set(bodyStr, path, "skip_thought_signature_validator")
				}
				return true
			})
		}
		return true
	})
	return []byte(bodyStr)
}

func setSessionBump(sessionID string, bump int) {
	if sessionID == "" {
		return
	}
	sessionBumps.Set(sessionID, bump)
}
