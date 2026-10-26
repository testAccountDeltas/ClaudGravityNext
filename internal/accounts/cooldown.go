// Part of the accounts package — see pool.go for the package doc comment.
// Holds cooldown bookkeeping: the account-wide cooldown (403 / 503 / OAuth
// failures) and the per-(account, model) cooldown from upstream 429s.
package accounts

import (
	"strings"
	"time"

	"claudgravity/internal/logx"
)

// modelKey normalizes a resolved model name into the ModelCooldowns key.
func modelKey(model string) string { return strings.ToLower(strings.TrimSpace(model)) }

// MarkAccountCooldown cools the whole account (every model) for duration.
// Used for account-level failures; quota 429s use MarkModelCooldown.
func (am *AccountManager) MarkAccountCooldown(email string, duration time.Duration) {
	am.mu.Lock()
	if acc, ok := am.accounts[email]; ok {
		// Only ever extend: a short cooldown (10s refresh 429) landing after a
		// long one (5-minute dead token, 403) must not shorten it.
		until := time.Now().Add(duration)
		if until.After(acc.CooldownUntil) {
			acc.CooldownUntil = until
		}
		if until.After(acc.hardCooldownUntil) {
			acc.hardCooldownUntil = until
		}
		logx.LogGateway("[AccountManager] Account %s put in cooldown for %v", email, duration)
	}
	am.mu.Unlock()

	globalSelector.UnstickAccount(email)
	if b := currentBroadcaster(); b != nil {
		b.BroadcastAccounts(am.GetAccounts())
	}
}

// MarkModelCooldown cools ONE (account, model) pair; the account stays
// usable for every other model. An empty model falls back to an
// account-wide cooldown. A later existing cooldown for the pair wins.
func (am *AccountManager) MarkModelCooldown(email, model string, duration time.Duration) {
	key := modelKey(model)
	if key == "" {
		am.MarkAccountCooldown(email, duration)
		return
	}
	am.mu.Lock()
	if acc, ok := am.accounts[email]; ok {
		until := time.Now().Add(duration)
		if acc.ModelCooldowns == nil {
			acc.ModelCooldowns = make(map[string]time.Time)
		}
		if until.After(acc.ModelCooldowns[key]) {
			acc.ModelCooldowns[key] = until
		}
		logx.LogGateway("[AccountManager] Account %s model %s put in cooldown for %v (429)", email, key, duration)
	}
	am.mu.Unlock()
	if b := currentBroadcaster(); b != nil {
		b.BroadcastAccounts(am.GetAccounts())
	}
}

// modelCooldownUntil returns when acc may serve model again, considering
// only the model's own cooldown (zero when not cooling).
func modelCooldownUntil(acc *Record, model string, now time.Time) time.Time {
	key := modelKey(model)
	if key == "" {
		return time.Time{}
	}
	if t := acc.ModelCooldowns[key]; t.After(now) {
		return t
	}
	return time.Time{}
}

// availableFor reports whether acc may serve model right now: account-wide
// gates (cooldown, quota) plus the model's own cooldown.
func availableFor(acc *Record, model string) bool {
	return hasRemainingQuota(acc) && modelCooldownUntil(acc, model, time.Now()).IsZero()
}

// EarliestAvailability returns the soonest moment any non-disabled account
// can serve model again (for a truthful Retry-After). Zero when at least one
// account is available now or the pool is empty.
func (as *AccountSelector) EarliestAvailability(model string) time.Time {
	now := time.Now()
	var best time.Time
	for _, acc := range globalAccountManager.GetAccounts() {
		if acc.Disabled {
			continue
		}
		at := now
		if acc.CooldownUntil.After(at) {
			at = acc.CooldownUntil
		}
		if q := quotaBlockUntil(acc.Quotas, now); q.After(at) {
			at = q
		}
		if m := modelCooldownUntil(acc, model, now); m.After(at) {
			at = m
		}
		if !at.After(now) {
			return time.Time{}
		}
		if best.IsZero() || at.Before(best) {
			best = at
		}
	}
	return best
}

// AvailableAtFor is AvailableAt plus the (account, model) cooldown a 429 on that
// model left behind. An empty model means no model-specific gate.
func (a *Record) AvailableAtFor(model string, now time.Time) time.Time {
	at := a.AvailableAt(now)
	if m := modelCooldownUntil(a, model, now); m.After(at) {
		at = m
	}
	return at
}

// AvailableAt returns the moment the account stops being blocked by its
// account-wide cooldown and by exhausted quota buckets; now (or earlier) means
// it is usable. Per-model cooldowns are not included (see EarliestAvailability).
func (a *Record) AvailableAt(now time.Time) time.Time {
	at := now
	if a.CooldownUntil.After(at) {
		at = a.CooldownUntil
	}
	if q := quotaBlockUntil(a.Quotas, now); q.After(at) {
		at = q
	}
	return at
}
