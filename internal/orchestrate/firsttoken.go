// Part of the orchestrate package — see pipeline.go for the package doc
// comment. Measures the time to the first streamed byte of one upstream
// attempt and reports it to the account selector.
package orchestrate

import (
	"sync/atomic"
	"time"

	"claudgravity/internal/accounts"
)

type firstTokenProbe struct {
	email string
	start time.Time
	seen  atomic.Bool
	touch func()
}

// newFirstTokenProbe wraps the watchdog's activity callback. acc may be nil
// (direct API-key traffic), in which case nothing is recorded.
func newFirstTokenProbe(acc *accounts.Record, start time.Time, touch func()) *firstTokenProbe {
	p := &firstTokenProbe{start: start, touch: touch}
	if acc != nil {
		p.email = acc.Email
	}
	return p
}

// Activity is passed as the stream's onActivity callback; the first call is
// the first token.
func (p *firstTokenProbe) Activity() {
	if p.seen.CompareAndSwap(false, true) {
		accounts.RecordFirstToken(p.email, time.Since(p.start))
	}
	p.touch()
}

// Timeout records an attempt the idle watchdog killed before any byte arrived:
// the wait so far is the account's first-token time (at least).
func (p *firstTokenProbe) Timeout() {
	if p.seen.CompareAndSwap(false, true) {
		accounts.RecordFirstToken(p.email, time.Since(p.start))
	}
}
