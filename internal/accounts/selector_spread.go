// Part of the accounts package — see pool.go for the package doc comment.
// Bounds the load skew that session/API-key stickiness produces: one
// account used to take ~60% of all requests (314 of 524), burn through its
// quota and drag the pool into mass cooldown.
package accounts

const (
	recentPicksWindow = 64   // last N selections tracked
	spreadMinSamples  = 12   // do not judge skew on fewer samples
	spreadShareLimit  = 0.5  // sticky account above this share of recent picks counts as overloaded
	spreadQuotaMargin = 0.25 // sticky account this far below the best candidate's quota fraction loses new sessions
	spreadMinFraction = 0.1  // never spread onto an account this close to exhaustion
)

// recordPickLocked remembers that email was just selected (as.mu held).
func (as *AccountSelector) recordPickLocked(email string) {
	as.recentPicks = append(as.recentPicks, email)
	if n := len(as.recentPicks); n > recentPicksWindow {
		as.recentPicks = append(as.recentPicks[:0], as.recentPicks[n-recentPicksWindow:]...)
	}
}

func (as *AccountSelector) recentCountLocked(email string) int {
	n := 0
	for _, e := range as.recentPicks {
		if e == email {
			n++
		}
	}
	return n
}

// spreadAlternativeLocked returns a healthier account to give a NEW session
// to instead of the sticky one, or nil to keep the sticky account. It
// applies only on the API-key stickiness path (ongoing conversations are
// served earlier by session affinity and never get here). The sticky
// account is passed over when its quota fraction trails the best candidate
// by spreadQuotaMargin, or when it took more than spreadShareLimit of the
// recent picks while another candidate took fewer.
func (as *AccountSelector) spreadAlternativeLocked(cur *Record, list []*Record, isExcluded func(*Record) bool, model string) *Record {
	curFrac := getEffectiveQuotaFraction(cur)
	curCount := as.recentCountLocked(cur.Email)
	skewed := len(as.recentPicks) >= spreadMinSamples &&
		float64(curCount)/float64(len(as.recentPicks)) > spreadShareLimit

	var bestFrac, leastUsed *Record
	for _, acc := range list {
		if acc.Email == cur.Email || isExcluded(acc) || !availableFor(acc, model) {
			continue
		}
		if getEffectiveQuotaFraction(acc) < spreadMinFraction {
			continue
		}
		if bestFrac == nil || getEffectiveQuotaFraction(acc) > getEffectiveQuotaFraction(bestFrac) {
			bestFrac = acc
		}
		if leastUsed == nil || as.recentCountLocked(acc.Email) < as.recentCountLocked(leastUsed.Email) ||
			(as.recentCountLocked(acc.Email) == as.recentCountLocked(leastUsed.Email) &&
				getEffectiveQuotaFraction(acc) > getEffectiveQuotaFraction(leastUsed)) {
			leastUsed = acc
		}
	}
	if bestFrac != nil && getEffectiveQuotaFraction(bestFrac)-curFrac > spreadQuotaMargin {
		return bestFrac
	}
	if skewed && leastUsed != nil && as.recentCountLocked(leastUsed.Email) < curCount {
		return leastUsed
	}
	return nil
}
