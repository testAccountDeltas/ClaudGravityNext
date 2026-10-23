// Part of the accounts package — see pool.go for the package doc
// comment. Split out of selector.go to keep selector.go under the 400-line
// ceiling: holds per-account quota fraction evaluation and expiration checks
// used during account candidate ranking.
package accounts

import (
	"strings"
	"time"
)

// exhaustedFraction is the remaining-quota fraction at or below which a bucket
// counts as exhausted. It was 0.05, which cooled down every account with 3-5%
// of its weekly limit left until the weekly reset, even though the upstream
// (and the original agy client) still serves requests there. Real exhaustion
// is also caught by the upstream 429 path, so only the last 1% is reserved.
const exhaustedFraction = 0.01

func hasRemainingQuota(acc *Record) bool {
	// If account is under cooldown due to quota exhaustion, check if resetTime has arrived
	if !acc.CooldownUntil.IsZero() && time.Now().Before(acc.CooldownUntil) {
		return false
	}

	if len(acc.Quotas) == 0 {
		return true // assume ok if unprobed
	}
	now := time.Now()
	for _, grp := range acc.Quotas {
		// An audit caught this used a WHITELIST (only "gemini"-named
		// groups) while getEffectiveQuotaFraction below uses a BLACKLIST
		// (every group except "claude"-named ones) — for a quota group
		// matching neither substring (e.g. "Cloud AI Companion"), this
		// function silently ignored it entirely while the other function
		// used it to drag the account's ranked fraction down, so the two
		// disagreed on the SAME account: this one said "has quota," the
		// other said "nearly exhausted." Unified to the blacklist
		// approach, which has an actual documented reason (exclude only
		// the known-irrelevant 3P Claude bucket) rather than an
		// unexplained narrow whitelist that silently dropped other real
		// Gemini-adjacent quota groups.
		if !strings.Contains(strings.ToLower(grp.DisplayName), "claude") {
			for _, b := range grp.Buckets {
				if b.RemainingFraction <= exhaustedFraction {
					// This bucket's own fraction is a snapshot from the
					// last FetchQuota — nothing re-fetches it on a timer
					// (RefreshAllQuotas only runs off a manual dashboard
					// button), so without checking the bucket's own
					// ResetTime here, an account stayed excluded from the
					// pool forever after its window reset, until someone
					// happened to click refresh. CooldownUntil above is the
					// same ResetTime baked in at the time of that last
					// fetch, so it goes stale in exactly the same way.
					if b.ResetTime != "" {
						if t, err := time.Parse(time.RFC3339Nano, b.ResetTime); err == nil && now.After(t) {
							continue
						}
						if t, err := time.Parse(time.RFC3339, b.ResetTime); err == nil && now.After(t) {
							continue
						}
					}
					return false
				}
			}
		}
	}
	return true
}

func getEffectiveQuotaFraction(acc *Record) float64 {
	if !acc.CooldownUntil.IsZero() && time.Now().Before(acc.CooldownUntil) {
		return 0.0
	}
	if len(acc.Quotas) == 0 {
		return 0.5
	}
	// Same "claude"-group exclusion as FetchQuota's CooldownUntil calc and
	// pickFiveHourAndWeekly (future internal/httpapi) below — a free-tier
	// account's 3P (Claude/GPT) bucket can legitimately sit at 0% for days
	// while Gemini itself is still wide open. Without filtering to the
	// Gemini group here, that unrelated 3P bucket dragged minFrac to ~0
	// for nearly every real account, making this ranking signal
	// meaningless (every account looks equally "almost exhausted"
	// regardless of its actual Gemini quota).
	minFrac := 1.0
	for _, grp := range acc.Quotas {
		if strings.Contains(strings.ToLower(grp.DisplayName), "claude") {
			continue
		}
		for _, b := range grp.Buckets {
			frac := b.RemainingFraction
			if frac <= exhaustedFraction && b.ResetTime != "" {
				if t, err := time.Parse(time.RFC3339Nano, b.ResetTime); err == nil && time.Now().After(t) {
					frac = 1.0
				} else if t, err := time.Parse(time.RFC3339, b.ResetTime); err == nil && time.Now().After(t) {
					frac = 1.0
				}
			}
			if frac < minFrac {
				minFrac = frac
			}
		}
	}
	return minFrac
}
