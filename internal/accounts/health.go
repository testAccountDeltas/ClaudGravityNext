// Part of the accounts package — see pool.go for the package doc comment.
// Holds the structured per-account health signal the dashboard renders:
// WHY an account is unusable (quota, ban, revoked token, ...) and until
// when, instead of only a free-form LastError string from the quota poll.
package accounts

import (
	"strings"
	"time"
)

// Error kinds surfaced to the dashboard as Record.ErrorKind.
const (
	ErrKindQuota        = "quota_exhausted" // upstream 429 / RESOURCE_EXHAUSTED
	ErrKindRateLimit    = "rate_limited"    // short-lived 429 (per-minute / refresh)
	ErrKindForbidden    = "forbidden"       // 403: banned, suspended, region/ToS-gated
	ErrKindAuthExpired  = "auth_expired"    // upstream 401, token will be re-fetched
	ErrKindTokenRevoked = "token_revoked"   // invalid_grant & co: needs re-login
	ErrKindOverloaded   = "overloaded"      // 503 / 529
	ErrKindNetwork      = "network"         // timeout, DNS, connection refused
	ErrKindOther        = "other"
)

// stickyErrorKinds never clear on a successful quota poll alone: the quota
// endpoint can answer fine for an account whose inference is banned, so a
// green poll proves nothing about them. They expire via ErrorUntil.
var stickyErrorKinds = map[string]bool{ErrKindForbidden: true, ErrKindTokenRevoked: true}

// ClassifyError maps an HTTP status and/or error text to an error kind.
func ClassifyError(status int, text string) string {
	t := strings.ToLower(text)
	switch {
	case strings.Contains(t, "invalid_grant"), strings.Contains(t, "unauthorized_client"),
		strings.Contains(t, "invalid_client"), strings.Contains(t, "no refresh token"):
		return ErrKindTokenRevoked
	case status == 403:
		return ErrKindForbidden
	case status == 401:
		return ErrKindAuthExpired
	case status == 429 && strings.Contains(t, "refresh"):
		return ErrKindRateLimit
	case status == 429, strings.Contains(t, "resource_exhausted"):
		return ErrKindQuota
	case status == 503, status == 529, strings.Contains(t, "overloaded"):
		return ErrKindOverloaded
	case status == 0 && (strings.Contains(t, "timeout") || strings.Contains(t, "deadline") ||
		strings.Contains(t, "connection") || strings.Contains(t, "no such host") ||
		strings.Contains(t, "eof")):
		return ErrKindNetwork
	}
	return ErrKindOther
}

// RecordAccountError stores a classified error on the account. until is when
// the condition is expected to clear (zero if unknown).
func (am *AccountManager) RecordAccountError(email string, status int, text string, until time.Time) {
	kind := ClassifyError(status, text)
	if len(text) > 300 {
		text = text[:300]
	}
	am.mu.Lock()
	if acc, ok := am.accounts[email]; ok {
		acc.ErrorKind = kind
		acc.ErrorDetail = text
		acc.ErrorAt = time.Now()
		acc.ErrorUntil = until
	}
	am.mu.Unlock()
	if broadcaster != nil {
		broadcaster.BroadcastAccounts(am.GetAccounts())
	}
}

// clearTransientErrorLocked is called with am.mu held after a successful quota
// poll: drops transient errors, keeps sticky ones until ErrorUntil passes.
func clearTransientErrorLocked(acc *Record) {
	if stickyErrorKinds[acc.ErrorKind] && time.Now().Before(acc.ErrorUntil) {
		return
	}
	acc.ErrorKind, acc.ErrorDetail = "", ""
	acc.ErrorAt, acc.ErrorUntil = time.Time{}, time.Time{}
}

// statusFromErr extracts the HTTP status from this package's own
// "HTTP 403 from host: ..." / "refresh failed (429): ..." error texts.
func statusFromErr(err error) int {
	s := err.Error()
	for _, pre := range []string{"HTTP ", "refresh failed (", "refresh rate-limited ("} {
		if i := strings.Index(s, pre); i >= 0 {
			n := 0
			for _, c := range s[i+len(pre):] {
				if c < '0' || c > '9' {
					break
				}
				n = n*10 + int(c-'0')
			}
			if n >= 100 {
				return n
			}
		}
	}
	return 0
}
