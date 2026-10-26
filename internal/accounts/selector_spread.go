// Part of the accounts package — see pool.go for the package doc comment.
// Bounds the load skew that session/API-key stickiness produces: one
// account used to take ~60% of all requests (314 of 524), burn through its
// quota and drag the pool into mass cooldown.
package accounts

import "time"

const (
	// A burst of parallel subagents is a burst of NEW conversations: each has
	// its own anchor, finds no affinity, and falls to the per-API-key sticky
	// account, so the whole fleet used to land on one account before the share
	// rule above (counted over every pick, hits included) noticed. A new
	// conversation shares no cache with its siblings, so spreading them costs
	// nothing and keeps one account's rate limit and quota from taking all of
	// the load.
	newConvBurstWindow = 60 * time.Second
	newConvBurstLimit  = 3 // new conversations one account may take per window before the next goes elsewhere

	recentPicksWindow = 64   // last N selections tracked
	spreadMinSamples  = 12   // do not judge skew on fewer samples
	spreadShareLimit  = 0.5  // sticky account above this share of recent picks counts as overloaded
	spreadQuotaMargin = 0.25 // sticky account this far below the best candidate's quota fraction loses new sessions
	spreadMinFraction = 0.1  // never spread onto an account this close to exhaustion
)

type newConvPick struct {
	email string
	at    time.Time
}

// recordNewConvLocked remembers that a NEW conversation was just given email.
func (as *AccountSelector) recordNewConvLocked(email string, now time.Time) {
	kept := as.newConvPicks[:0]
	for _, p := range as.newConvPicks {
		if now.Sub(p.at) < newConvBurstWindow {
			kept = append(kept, p)
		}
	}
	as.newConvPicks = append(kept, newConvPick{email: email, at: now})
}

func (as *AccountSelector) recentNewConvCountLocked(email string, now time.Time) int {
	n := 0
	for _, p := range as.newConvPicks {
		if p.email == email && now.Sub(p.at) < newConvBurstWindow {
			n++
		}
	}
	return n
}

// burstAlternativeLocked returns the account to give the next NEW conversation
// to when cur already took newConvBurstLimit of them within the window, or nil.
func (as *AccountSelector) burstAlternativeLocked(cur *Record, list []*Record, isExcluded func(*Record) bool, model string, now time.Time) *Record {
	curCount := as.recentNewConvCountLocked(cur.Email, now)
	if curCount < newConvBurstLimit {
		return nil
	}
	var best *Record
	bestCount := curCount
	for _, acc := range list {
		if acc.Email == cur.Email || isExcluded(acc) || !availableFor(acc, model) || getEffectiveQuotaFraction(acc) < spreadMinFraction {
			continue
		}
		c := as.recentNewConvCountLocked(acc.Email, now)
		if c < bestCount || (best != nil && c == bestCount && getEffectiveQuotaFraction(acc) > getEffectiveQuotaFraction(best)) {
			best, bestCount = acc, c
		}
	}
	return best
}

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

// noteNewConvLocked counts a pick that started a conversation (one with an
// anchor); picks without an anchor cannot be told apart and are not counted.
func (as *AccountSelector) noteNewConvLocked(conversationKey, email string, now time.Time) {
	if conversationKey != "" {
		as.recordNewConvLocked(email, now)
	}
}
