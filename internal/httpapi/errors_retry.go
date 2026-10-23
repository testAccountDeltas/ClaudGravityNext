// Part of the httpapi package — see errors.go for the package doc
// comment. Retry-After derivation and client-message sanitising for
// error responses.
package httpapi

import (
	"net/http"
	"strings"
	"time"

	"claudgravity/internal/accounts"
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
		if a.CooldownUntil.After(now) {
			d := a.CooldownUntil.Sub(now)
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

const upstreamRetryPrefix = "upstream failed after retries:"

// ClientUpstreamMessage turns an internal upstream failure string into a
// short message safe to show a client: wrapper prefixes (possibly doubled)
// are removed and anything that looks like raw Google JSON/RPC detail is
// replaced by a status-based sentence. The full text must be logged by the
// caller.
func ClientUpstreamMessage(status int, errText string) string {
	msg := strings.TrimSpace(errText)
	for {
		l := strings.ToLower(msg)
		switch {
		case strings.HasPrefix(l, upstreamRetryPrefix):
			msg = strings.TrimSpace(msg[len(upstreamRetryPrefix):])
		case strings.HasPrefix(l, "upstream failed after retries"):
			msg = strings.TrimSpace(strings.TrimLeft(msg[len("upstream failed after retries"):], ": "))
		default:
			goto done
		}
	}
done:
	raw := strings.ContainsAny(msg, "{}[]") || strings.Contains(msg, "googleapis") ||
		strings.Contains(msg, "RESOURCE_EXHAUSTED") || strings.Contains(msg, "\n") || len(msg) > 300
	if msg != "" && !raw {
		return msg
	}
	switch {
	case status == http.StatusTooManyRequests:
		return "Rate limited by the upstream provider. Please retry later."
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "The upstream provider rejected the request's credentials."
	case status >= 400 && status < 500:
		return "The upstream provider rejected the request."
	case status == 529 || status == http.StatusServiceUnavailable || status == http.StatusBadGateway || status == http.StatusGatewayTimeout:
		return "The upstream provider is temporarily unavailable. Please retry."
	}
	return "Upstream request failed. Please retry."
}
