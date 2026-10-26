// Part of the httpapi package — see errors.go for the package doc
// comment. Retry-After derivation and client-message sanitising for
// error responses.
package httpapi

import (
	"net/http"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/anthropicerr"
)

const (
	maxRetryAfter     = 900 * time.Second
	defaultRetryAfter = 900 * time.Second // no pool information at all
	idleRetryAfter    = 30 * time.Second  // pool exists, nothing cooling down
)

// poolRetryAfter returns how long a rate-limited client should wait: the
// time until the soonest enabled account leaves cooldown, clamped to
// [1s, 15min]. With an empty/unavailable pool it falls back to 15 minutes
// (the old fixed value); with a healthy pool and no cooldown (the 429 came
// from elsewhere) it returns a short 30s.
func poolRetryAfter() time.Duration {
	mgr := accounts.Manager()
	if mgr == nil {
		return defaultRetryAfter
	}
	return retryAfterFromRecords(mgr.GetAccounts(), time.Now())
}

func retryAfterFromRecords(recs []*accounts.Record, now time.Time) time.Duration {
	if len(recs) == 0 {
		return defaultRetryAfter
	}
	var soonest time.Duration
	found, anyEnabled := false, false
	for _, a := range recs {
		if a == nil || a.Disabled {
			continue
		}
		anyEnabled = true
		// Account-wide cooldown AND exhausted quota buckets: an account whose
		// five-hour bucket is empty is not "usable right now" just because
		// its CooldownUntil has not been set.
		if at := a.AvailableAt(now); at.After(now) {
			d := at.Sub(now)
			if !found || d < soonest {
				soonest, found = d, true
			}
		} else {
			// An account that is usable right now: retry almost at once.
			return idleRetryAfter
		}
	}
	if !found {
		if anyEnabled {
			return idleRetryAfter
		}
		return defaultRetryAfter
	}
	if soonest < time.Second {
		soonest = time.Second
	}
	if soonest > maxRetryAfter {
		soonest = maxRetryAfter
	}
	return soonest.Round(time.Second)
}

// shouldRetry mirrors Anthropic's x-should-retry: true for 408/409/429 and
// every 5xx (including 529), false for other client errors.
func shouldRetry(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusConflict ||
		status == http.StatusTooManyRequests || status >= 500
}

// ClientUpstreamMessage is anthropicerr.ClientMessage (shared with the
// streaming path, which also has to keep raw upstream detail off the wire).
func ClientUpstreamMessage(status int, errText string) string {
	return anthropicerr.ClientMessage(status, errText)
}
