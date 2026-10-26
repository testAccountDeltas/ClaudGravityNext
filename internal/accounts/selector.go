// Part of the accounts package's port from superrelay (v1)'s
// selector.go — see pool.go for the split rationale. This file holds
// account selection; affinity.go holds the session-sticky LRU this
// selection strategy leans on.
package accounts

import (
	"container/list"
	"fmt"
	"sync"
	"time"

	"claudgravity/internal/config"
)

const maxStickyMapEntries = 1000

type AccountSelector struct {
	mu        sync.Mutex
	stickyMap map[string]string        // apiKey -> assigned email (fallback)
	stickyLRU *list.List               // front = most recently used apiKey
	stickyIdx map[string]*list.Element // apiKey -> node in stickyLRU

	// affinityLRU/affinityIdx together replace a plain
	// map[string]SessionAffinityEntry. A bare Go map has no iteration
	// order guarantee, so evicting "the oldest" out of it at capacity was
	// actually evicting an arbitrary entry — on a long-lived server this
	// could silently kick an ACTIVE conversation's account binding and
	// force an avoidable Google-side prompt-cache reset. list.List gives a
	// real most-recently-used-at-front order: MoveToFront on every hit,
	// evict from Back() when full, so eviction is deterministic and always
	// picks the conversation that's gone longest without being touched.
	affinityLRU   *list.List               // front = most recently used
	affinityIdx   map[string]*list.Element // conversationAnchor/sessionID -> its node in affinityLRU
	roundRobinIdx int
	recentPicks   []string // last recentPicksWindow selected emails (selector_spread.go)
	newConvPicks  []newConvPick
}

var globalSelector = &AccountSelector{
	stickyMap:   make(map[string]string),
	stickyLRU:   list.New(),
	stickyIdx:   make(map[string]*list.Element),
	affinityLRU: list.New(),
	affinityIdx: make(map[string]*list.Element),
}

// GlobalSelector exposes the package-level AccountSelector singleton to
// internal/orchestrate (account selection/affinity/cooldown during the
// pre-stream failover loop) — same accessor convention as Manager().
func GlobalSelector() *AccountSelector { return globalSelector }

// NewAccountSelector exists for tests that need an isolated selector
// instance instead of mutating the package-level singleton.
func NewAccountSelector() *AccountSelector {
	return &AccountSelector{
		stickyMap:   make(map[string]string),
		stickyLRU:   list.New(),
		stickyIdx:   make(map[string]*list.Element),
		affinityLRU: list.New(),
		affinityIdx: make(map[string]*list.Element),
	}
}

func (as *AccountSelector) setStickyLocked(apiKey, email string) {
	if apiKey == "" || email == "" {
		return
	}
	if as.stickyMap == nil {
		as.stickyMap = make(map[string]string)
	}
	if as.stickyLRU == nil {
		as.stickyLRU = list.New()
		as.stickyIdx = make(map[string]*list.Element)
	}
	if el, ok := as.stickyIdx[apiKey]; ok {
		as.stickyLRU.MoveToFront(el)
	} else {
		for len(as.stickyMap) >= maxStickyMapEntries {
			back := as.stickyLRU.Back()
			if back != nil {
				oldKey := back.Value.(string)
				as.stickyLRU.Remove(back)
				delete(as.stickyIdx, oldKey)
				delete(as.stickyMap, oldKey)
			} else {
				for k := range as.stickyMap {
					delete(as.stickyMap, k)
					break
				}
			}
		}
		as.stickyIdx[apiKey] = as.stickyLRU.PushFront(apiKey)
	}
	as.stickyMap[apiKey] = email
}

// SelectAccount chooses an account for a request based on tenant strategy,
// prioritizing Session Affinity by Conversation Anchor for maximum prompt caching.
func (as *AccountSelector) SelectAccount(apiKey string, conversationKey string, tenant config.TenantConfig) (*Record, error) {
	return as.SelectAccountExcluding(apiKey, conversationKey, tenant, nil)
}

// SelectAccountExcluding chooses an account while skipping accounts in the excluded map (useful for Failover).
func (as *AccountSelector) SelectAccountExcluding(apiKey string, conversationKey string, tenant config.TenantConfig, excluded map[string]bool) (*Record, error) {
	return as.SelectAccountForModel(apiKey, conversationKey, tenant, excluded, "")
}

// SelectAccountForModel is SelectAccountExcluding gated on the target model:
// an account whose (account, model) pair is cooling down after a 429 is
// skipped for that model only. An empty model applies account-wide gates only.
func (as *AccountSelector) SelectAccountForModel(apiKey string, conversationKey string, tenant config.TenantConfig, excluded map[string]bool, model string) (*Record, error) {
	// Deep-copies every account's Quotas slice — fetched before taking
	// as.mu (it only touches AccountManager's own lock, nothing in as)
	// so this snapshot cost isn't paid while holding the selector's lock
	// and serializing every OTHER concurrent request's own account pick
	// behind it.
	accountList := globalAccountManager.GetAccounts()

	as.mu.Lock()
	defer as.mu.Unlock()

	acc, err := as.selectLocked(apiKey, conversationKey, tenant, excluded, model, accountList)
	if err == nil && acc != nil {
		as.recordPickLocked(acc.Email)
	}
	return acc, err
}

func (as *AccountSelector) selectLocked(apiKey string, conversationKey string, tenant config.TenantConfig, excluded map[string]bool, model string, accountList []*Record) (*Record, error) {
	if len(accountList) == 0 {
		return nil, fmt.Errorf("no accounts available in pool")
	}

	// Filter out excluded accounts
	isExcluded := func(acc *Record) bool {
		return acc.Disabled || (excluded != nil && excluded[acc.Email])
	}

	// 1. Explicitly pinned in config (Preferred account)
	// Must have remaining quota and not be in cooldown or excluded.
	if tenant.StickyAccount != "" {
		for _, acc := range accountList {
			if acc.Email == tenant.StickyAccount && !isExcluded(acc) && availableFor(acc, model) {
				return acc, nil
			}
		}
	}

	now := time.Now()

	// 2. Session Affinity by Conversation Anchor (Highest Priority for prompt cache).
	// If tenant explicitly configured strategy is round_robin, bypass session affinity
	// so round-robin pool distribution is strictly honored.
	if tenant.Strategy != "round_robin" && conversationKey != "" {
		if el, ok := as.affinityIdx[conversationKey]; ok {
			entry := el.Value.(*affinityLRUEntry)
			if now.Before(entry.expiresAt) {
				for _, acc := range accountList {
					if acc.Email == entry.email && !isExcluded(acc) && availableFor(acc, model) {
						// Refresh lease and mark as most-recently-used.
						entry.expiresAt = now.Add(sessionAffinityTTL)
						as.affinityLRU.MoveToFront(el)
						return acc, nil
					}
				}
			} else {
				as.affinityLRU.Remove(el)
				delete(as.affinityIdx, conversationKey)
			}
		}
	}

	// 3. Fallback: Sticky per API Key (when strategy is sticky or default).
	// If preferred account is unavailable or not set, stick to the active assigned account for this tenant
	// without randomizing requests across all accounts.
	if (tenant.Strategy == "sticky" || tenant.Strategy == "") && apiKey != "" {
		if assignedEmail, ok := as.stickyMap[apiKey]; ok {
			if as.stickyLRU != nil && as.stickyIdx != nil {
				if el, exists := as.stickyIdx[apiKey]; exists {
					as.stickyLRU.MoveToFront(el)
				}
			}
			for _, acc := range accountList {
				if acc.Email == assignedEmail && !isExcluded(acc) && availableFor(acc, model) {
					// Bound sticky skew for new sessions (selector_spread.go).
					if alt := as.spreadAlternativeLocked(acc, accountList, isExcluded, model); alt != nil {
						as.setStickyLocked(apiKey, alt.Email)
						as.noteNewConvLocked(conversationKey, alt.Email, now)
						return alt, nil
					}
					// A burst of new conversations (parallel subagents) is spread
					// over the pool instead of all taking the sticky account.
					if conversationKey != "" {
						if alt := as.burstAlternativeLocked(acc, accountList, isExcluded, model, now); alt != nil {
							as.noteNewConvLocked(conversationKey, alt.Email, now)
							return alt, nil
						}
					}
					as.noteNewConvLocked(conversationKey, acc.Email, now)
					return acc, nil
				}
			}
		}
	}

	// 4. If tenant strategy is explicitly round_robin, distribute across the pool using round-robin among healthy accounts.
	if tenant.Strategy == "round_robin" {
		valid := make([]*Record, 0)
		for _, acc := range accountList {
			if !isExcluded(acc) && availableFor(acc, model) {
				valid = append(valid, acc)
			}
		}
		if len(valid) > 0 {
			// Index first, then advance: bumping first skipped valid[0] on
			// the very first pick.
			pick := valid[as.roundRobinIdx%len(valid)]
			as.roundRobinIdx++
			return pick, nil
		}
	}

	// 4b. Select the best available account based on remaining quota among healthy accounts.
	// When strategy is sticky, bind this chosen account to stickyMap so subsequent requests stick to it.
	var bestAcc *Record
	var maxQuota float64 = -1.0

	for _, acc := range accountList {
		if isExcluded(acc) || !availableFor(acc, model) {
			continue
		}
		q := getEffectiveQuotaFraction(acc)
		if q > maxQuota {
			maxQuota = q
			bestAcc = acc
		}
	}

	if bestAcc != nil && maxQuota > 0 {
		if apiKey != "" && (tenant.Strategy == "sticky" || tenant.Strategy == "") {
			as.setStickyLocked(apiKey, bestAcc.Email)
		}
		return bestAcc, nil
	}

	// 5. Fallback: Round-Robin across any non-excluded accounts not
	// currently in cooldown.
	//
	// An audit caught that this filtered only on isExcluded (accounts
	// already tried THIS request) and never checked CooldownUntil at
	// all — reaching this step means step 4b's maxQuota>0 check already
	// failed, i.e. every account's effective quota fraction is at or near
	// zero, overwhelmingly because they're all in cooldown. Without this
	// check, every one of the caller's retry attempts picked a
	// guaranteed-to-fail cooling-down account and burned a real network
	// round-trip on it, instead of recognizing the pool is genuinely
	// exhausted and failing fast with 429.
	valid := make([]*Record, 0)
	for _, acc := range accountList {
		if !isExcluded(acc) && (acc.CooldownUntil.IsZero() || !now.Before(acc.CooldownUntil)) && modelCooldownUntil(acc, model, now).IsZero() {
			valid = append(valid, acc)
		}
	}

	if len(valid) == 0 {
		return nil, fmt.Errorf("all accounts are disabled, cooling down or excluded")
	}

	chosen := valid[as.roundRobinIdx%len(valid)]
	as.roundRobinIdx++
	return chosen, nil
}

// GetOrAssignStickyAccount guarantees returning the assigned sticky account for an API key.
// If not yet bound, it deterministically picks the best non-exhausted account and pins it permanently to the key.
func (as *AccountSelector) GetOrAssignStickyAccount(apiKey string, tenant config.TenantConfig) *Record {
	accountList := globalAccountManager.GetAccounts()

	as.mu.Lock()
	defer as.mu.Unlock()

	if len(accountList) == 0 {
		return nil
	}

	// 1. Explicitly pinned in tenant config (must have quota)
	if tenant.StickyAccount != "" {
		for _, acc := range accountList {
			if acc.Email == tenant.StickyAccount && !acc.Disabled && hasRemainingQuota(acc) {
				return acc
			}
		}
	}

	// 2. Previously assigned sticky account for this client API key
	if apiKey != "" {
		if assignedEmail, ok := as.stickyMap[apiKey]; ok {
			if as.stickyLRU != nil && as.stickyIdx != nil {
				if el, exists := as.stickyIdx[apiKey]; exists {
					as.stickyLRU.MoveToFront(el)
				}
			}
			for _, acc := range accountList {
				if acc.Email == assignedEmail && !acc.Disabled && hasRemainingQuota(acc) {
					return acc
				}
			}
		}
	}

	// 3. Select best available non-exhausted account
	var bestAcc *Record
	var maxQuota float64 = -1.0

	for _, acc := range accountList {
		if acc.Disabled || !hasRemainingQuota(acc) {
			continue
		}
		q := getEffectiveQuotaFraction(acc)
		if q > maxQuota {
			maxQuota = q
			bestAcc = acc
		}
	}

	if bestAcc != nil && maxQuota > 0 {
		if apiKey != "" {
			as.setStickyLocked(apiKey, bestAcc.Email)
		}
		return bestAcc
	}

	// 4. Fallback: first non-disabled, non-cooling-down account.
	//
	// An audit caught that this step only ever checked acc.Disabled,
	// never acc.CooldownUntil — unlike hasRemainingQuota (used by steps 2
	// and 3 above) and SelectAccountExcluding's own analogous last-resort
	// step, which both already treat a cooling-down account as
	// unavailable. Reaching here means step 3's maxQuota>0 check already
	// failed for every account, so a client could get permanently pinned
	// (via as.stickyMap) to an account that's currently in a 429 cooldown
	// and will keep failing until that cooldown expires, even once a
	// different account recovers. Prefer a non-cooling-down account here
	// first; only fall back to one that IS cooling down if every account
	// is (this function must never return nil).
	now := time.Now()
	for _, acc := range accountList {
		if !acc.Disabled && (acc.CooldownUntil.IsZero() || now.After(acc.CooldownUntil)) {
			if apiKey != "" {
				as.setStickyLocked(apiKey, acc.Email)
			}
			return acc
		}
	}
	for _, acc := range accountList {
		if !acc.Disabled {
			if apiKey != "" {
				as.setStickyLocked(apiKey, acc.Email)
			}
			return acc
		}
	}

	return accountList[0]
}
