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

// parseResetTime parses a bucket ResetTime; zero when absent or malformed.
func parseResetTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

func isClaudeGroup(g QuotaGroup) bool {
	return strings.Contains(strings.ToLower(g.DisplayName), "claude")
}

// quotaBlockUntil decides whether the non-Claude quota buckets make the
// whole account unusable, and until when (zero = usable). Live evidence: the
// upstream answers 429 for every Gemini model on an account whose "Gemini
// Models" bucket is at ~0 even when another bucket is full, so ANY exhausted
// non-Claude bucket blocks the account until its own reset time (the latest
// one when several are exhausted). A bucket whose ResetTime already passed
// counts as restored (nothing re-fetches the snapshot on a timer, so a stale
// one must not block forever).
func quotaBlockUntil(groups []QuotaGroup, now time.Time) time.Time {
	var until time.Time
	for _, grp := range groups {
		if isClaudeGroup(grp) {
			continue
		}
		for _, b := range grp.Buckets {
			if b.FractionUnknown || b.RemainingFraction > exhaustedFraction {
				continue
			}
			reset := parseResetTime(b.ResetTime)
			if !reset.IsZero() && !reset.After(now) {
				continue
			}
			if reset.IsZero() {
				reset = now.Add(15 * time.Minute)
			}
			if reset.After(until) {
				until = reset
			}
		}
	}
	return until
}

func hasRemainingQuota(acc *Record) bool {
	if !acc.CooldownUntil.IsZero() && time.Now().Before(acc.CooldownUntil) {
		return false
	}
	return quotaBlockUntil(acc.Quotas, time.Now()).IsZero()
}

func getEffectiveQuotaFraction(acc *Record) float64 {
	if !acc.CooldownUntil.IsZero() && time.Now().Before(acc.CooldownUntil) {
		return 0.0
	}
	if len(acc.Quotas) == 0 {
		return 0.5
	}
	// Claude groups are excluded (see quotaBlockUntil). Rank by the WORST
	// non-Claude bucket so a ~0 account sorts last even next to a full one.
	// A bucket whose reset already passed counts as restored.
	now := time.Now()
	minFrac := 1.0
	known := false
	for _, grp := range acc.Quotas {
		if isClaudeGroup(grp) {
			continue
		}
		for _, b := range grp.Buckets {
			if b.FractionUnknown {
				continue
			}
			known = true
			frac := b.RemainingFraction
			if frac <= exhaustedFraction {
				if t := parseResetTime(b.ResetTime); !t.IsZero() && now.After(t) {
					frac = 1.0
				}
			}
			if frac < minFrac {
				minFrac = frac
			}
		}
	}
	if !known {
		return 0.5 // no usable bucket: same neutral rank as "no quota data"
	}
	return minFrac
}
