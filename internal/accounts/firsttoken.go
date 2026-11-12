// Part of the accounts package — see pool.go for the package doc comment.
// Tracks how quickly each account produces its first streamed token, so the
// selector can steer new work away from an account Google is currently
// serving slowly (same model, same prompt: 3 s on one account, 60 s on
// another). Quota says nothing about this, and stickiness / conversation
// affinity otherwise pin a client to the slow account indefinitely.
package accounts

import (
	"sync"
	"time"

	"claudgravity/internal/logx"
)

const (
	// slowFirstTokenThreshold is the smoothed time-to-first-token above which an
	// account counts as slow. Healthy accounts measured 3-10 s with thinking on;
	// degraded ones 17-76 s.
	slowFirstTokenThreshold = 20 * time.Second
	// firstTokenAlpha weights the newest sample in the moving average.
	firstTokenAlpha = 0.4
	// slowRecheckAfter is how long an account stays shunned without fresh data
	// before it gets another request (Google's slowness comes and goes).
	slowRecheckAfter = 10 * time.Minute
	// slowSampleCap bounds one sample so a single 3-minute hang does not
	// need dozens of fast replies to recover from.
	slowSampleCap = 90 * time.Second
)

type firstTokenStat struct {
	ewma    time.Duration
	updated time.Time
	slow    bool
}

type firstTokenTracker struct {
	mu    sync.Mutex
	stats map[string]*firstTokenStat
	now   func() time.Time
}

var globalFirstToken = &firstTokenTracker{stats: make(map[string]*firstTokenStat), now: time.Now}

// RecordFirstToken feeds one observed time-to-first-token for an account.
func RecordFirstToken(email string, d time.Duration) { globalFirstToken.record(email, d) }

// IsSlowAccount reports whether the account's recent first-token times are
// above the slow threshold (and still fresh enough to trust).
func IsSlowAccount(email string) bool { return globalFirstToken.isSlow(email) }

// FirstTokenAverage returns the smoothed time-to-first-token, or 0 when unknown.
func FirstTokenAverage(email string) time.Duration { return globalFirstToken.average(email) }

func (t *firstTokenTracker) record(email string, d time.Duration) {
	if email == "" || d <= 0 {
		return
	}
	if d > slowSampleCap {
		d = slowSampleCap
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	s, ok := t.stats[email]
	if !ok || now.Sub(s.updated) > slowRecheckAfter {
		// First sample, or the old picture is stale: start over from this one.
		s = &firstTokenStat{ewma: d}
		t.stats[email] = s
	} else {
		s.ewma = time.Duration(firstTokenAlpha*float64(d) + (1-firstTokenAlpha)*float64(s.ewma))
	}
	s.updated = now
	slow := s.ewma > slowFirstTokenThreshold
	if slow != s.slow {
		s.slow = slow
		if slow {
			logx.LogGateway("[AccountManager] Account %s is slow (first token ~%s); routing new work elsewhere while others are faster", email, s.ewma.Round(time.Second))
		} else {
			logx.LogGateway("[AccountManager] Account %s is fast again (first token ~%s)", email, s.ewma.Round(time.Second))
		}
	}
}

func (t *firstTokenTracker) isSlow(email string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.stats[email]
	return ok && s.slow && t.now().Sub(s.updated) <= slowRecheckAfter
}

func (t *firstTokenTracker) average(email string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.stats[email]; ok {
		return s.ewma
	}
	return 0
}
