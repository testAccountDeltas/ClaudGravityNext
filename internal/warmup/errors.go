// Package warmup is a PARTIAL port of superrelay (v1)'s warmup.go, split
// by responsibility per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 4.3: errors.go
// (this file — the three upstream-error classifiers), payload.go
// (WrapAntigravityPayload + the Antigravity host list).
//
// NOT YET PORTED (deliberately): needsGeminiWeeklyWarmup,
// WarmupAllFreshAccounts, WarmupSingleAccount. Those three are methods on
// v1's *AccountManager / take a *AccountRecord — they structurally depend
// on internal/accounts, which doesn't exist yet (that's Phase 2 of the
// roadmap). Porting them now would mean fabricating a premature,
// throwaway accounts stub just to satisfy the compiler — worse than
// deferring. See ROADMAP_PROGRESS.md's Phase 1 entry for this file for
// the full reasoning; this is a real, load-bearing dependency discovered
// by attempting the actual port, not a shortcut.
package warmup

import (
	"net/http"
	"strings"
)

// isAntigravityLocationGateError reports whether an upstream response is a
// verdict on which exit host answered ("User location is not supported for
// the API use") rather than on the request itself — worth another host, not
// a hard failure.
func IsAntigravityLocationGateError(status int, errText string) bool {
	if status != http.StatusBadRequest || errText == "" {
		return false
	}
	return strings.Contains(strings.ToLower(errText), "user location is not supported")
}

// isTransientNetworkError reports whether a failed upstream call never got a
// real HTTP response at all — a DNS lookup failure, a dial timeout, a reset
// connection — as opposed to Google's own application-level 429/403/400
// rejecting the request. The caller's HTTP client failure surfaces as a
// synthetic http.StatusBadGateway with the raw Go net error text. This is a
// verdict on the network path to one host, not on the calling OAuth account
// at all — a DNS hiccup resolving one AntigravityHosts entry fails
// identically no matter which account's token is in the Authorization
// header, so routing it through account exclusion/cooldown (like a real
// 429/403) just burns through the whole account pool for a problem rotating
// accounts can never fix, while never trying the other host that might
// actually be reachable.
func IsTransientNetworkError(status int, errText string) bool {
	if status != http.StatusBadGateway || errText == "" {
		return false
	}
	lower := strings.ToLower(errText)
	for _, marker := range []string{"dial tcp", "lookup", "no such host", "connection reset", "connection refused", "i/o timeout", "eof", "operation was canceled", "context deadline exceeded"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// isSessionTokenWallError matches Google's "per-session ~1,048,576-token
// wall" 400 error — distinct from a genuine too-large-request rejection or a
// 429 quota error, neither of which a session bump can fix.
func IsSessionTokenWallError(status int, errText string) bool {
	if status != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(errText)
	return strings.Contains(lower, "exceeds the maximum number of tokens") ||
		strings.Contains(lower, "exceeds the maximum number of token") ||
		strings.Contains(lower, "1048576")
}
