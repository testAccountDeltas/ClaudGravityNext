// Part of the accounts package's port from superrelay (v1)'s
// selector.go — see selector.go for the split rationale. This file
// holds the session-sticky LRU affinity table.
package accounts

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	// sessionAffinityTTL outlives the longest prompt cache TTL (1h), so a
	// binding never lapses while its account can still hold the cache.
	sessionAffinityTTL        = 1 * time.Hour
	maxSessionAffinityEntries = 5000
)

// affinityLRUEntry is the value stored in each affinityLRU list.Element.
// Kept as a pointer-sized struct in the list itself (not a copy in the map)
// so lookups, refreshes and moves are all O(1) without re-allocating.
type affinityLRUEntry struct {
	key       string
	email     string
	expiresAt time.Time
}

// DeriveConversationAnchor creates a stable identifier for a multi-turn conversation.
// Every turn of the same conversation shares the same anchor (first user message + system prefix),
// allowing all turns to hit the exact same Google account for 100% prompt caching.
//
// headerSessionID is Claude Code's own terminal-session id
// (x-claude-code-session-id / x-session-id), which — unlike the first user
// message — never changes for the life of the conversation. Without it, a
// long conversation that crosses the context-truncation drop threshold
// gets its oldest messages replaced by a synthetic "[Gateway notice: ...]"
// message; on the very next turn THAT notice becomes the new "first user
// message", the hash changes, and the conversation gets silently rebound to
// a different Google account with none of the prior turns' prefix cache.
// instanceID, when non-empty, is a subagent's own stable per-instance key
// (see ParseAgentInfo) — headerSessionID alone is shared by the main thread
// and EVERY subagent in the conversation, so without qualifying by it here,
// Session Affinity pinned 5-10 parallel subagents to the exact same Google
// account (they share no content worth caching together anyway), piling
// all of their load onto one account's quota and triggering an immediate
// 429 instead of spreading across the pool the way the other per-instance
// keys already do.
func DeriveConversationAnchor(bodyBytes []byte, headerSessionID string, instanceID string) string {
	if headerSessionID != "" {
		if instanceID != "" {
			return "sid:" + headerSessionID + ":" + instanceID
		}
		return "sid:" + headerSessionID
	}

	root := gjson.ParseBytes(bodyBytes)

	// If client explicitly sent a session id, use it
	if sid := root.Get("metadata.session_id").String(); sid != "" {
		// An audit caught that this branch, unlike the header-based one
		// just above it, never appended instanceID — when the
		// x-claude-code-session-id header isn't set (so execution falls
		// through to the body's own metadata.session_id), every parallel
		// subagent of the same session shares this exact string, with no
		// instanceID to tell them apart. isSubagentKey downstream
		// requires >=2 colons to recognize a subagent key at all, so
		// these all collapsed onto the SAME conversation anchor — and
		// thus the same Google account — instantly hitting that one
		// account's 429 instead of spreading across the pool.
		if instanceID != "" {
			return "sid:" + sid + ":" + instanceID
		}
		return "sid:" + sid
	}

	// Find the first user message — skipping this gateway's own injected
	// truncation notices, so the anchor stays on the oldest surviving real
	// message instead of flipping to the notice text itself.
	firstUserMsg := ""
	msgs := root.Get("messages")
	if msgs.IsArray() {
		for _, m := range msgs.Array() {
			if m.Get("role").String() == "user" {
				c := m.Get("content")
				candidate := ""
				if c.Type == gjson.String {
					candidate = c.String()
				} else if c.IsArray() {
					for _, part := range c.Array() {
						if part.Get("type").String() == "text" {
							candidate = part.Get("text").String()
							break
						}
					}
				}
				if strings.HasPrefix(strings.TrimSpace(candidate), "[Gateway notice:") {
					continue
				}
				if candidate != "" {
					firstUserMsg = candidate
					break
				}
			}
		}
	}

	if firstUserMsg == "" {
		return ""
	}

	// Hash first user message (up to first 500 chars to avoid oversized inputs).
	// NOTE: We deliberately DO NOT include 'model' in the conversation hash!
	// Claude Code frequently sends auxiliary requests (summaries, titles) using sonnet/haiku
	// while main turns use opus/flash. Excluding model ensures 100% stable Session Affinity
	// and preserves prompt cache across all turns of the same conversation.
	cleanFirst := strings.TrimSpace(firstUserMsg)
	if len(cleanFirst) > 500 {
		cleanFirst = cleanFirst[:500]
	}

	h := sha256.Sum256([]byte(cleanFirst))
	anchor := "conv:" + hex.EncodeToString(h[:12])
	// An audit caught this last-resort fallback had the same instanceID
	// gap as the metadata.session_id branch above (already fixed in an
	// earlier round) — when BOTH the header and metadata.session_id are
	// absent, parallel subagents sharing an identical (or near-identical,
	// truncated-to-500-chars) first user message collapse onto this same
	// hash, and thus the same Google account.
	if instanceID != "" {
		return anchor + ":" + instanceID
	}
	return anchor
}

// GetAffinityAccount returns the assigned email for a conversation anchor or
// session id. A pure peek — doesn't refresh the LRU position or TTL, since
// callers use this to inspect the binding without necessarily acting on it.
func (as *AccountSelector) GetAffinityAccount(conversationKey string) string {
	if conversationKey == "" {
		return ""
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	if el, ok := as.affinityIdx[conversationKey]; ok {
		entry := el.Value.(*affinityLRUEntry)
		if time.Now().Before(entry.expiresAt) {
			return entry.email
		}
	}
	return ""
}

// RememberAffinity binds a conversation anchor to an account upon successful response.
func (as *AccountSelector) RememberAffinity(conversationKey, email string) {
	if conversationKey == "" || email == "" {
		return
	}
	as.mu.Lock()
	defer as.mu.Unlock()
	as.bindAffinityLocked(conversationKey, email, time.Now())
}

func (as *AccountSelector) bindAffinityLocked(key, email string, now time.Time) {
	if key == "" || email == "" {
		return
	}

	// Already bound: update in place and promote to most-recently-used.
	if el, ok := as.affinityIdx[key]; ok {
		entry := el.Value.(*affinityLRUEntry)
		entry.email = email
		entry.expiresAt = now.Add(sessionAffinityTTL)
		as.affinityLRU.MoveToFront(el)
		return
	}

	// New binding: make room first. Evict from the real tail (truly
	// least-recently-used, not a random map entry) — expired ones first,
	// since those are free wins, then plain LRU order if still full.
	for len(as.affinityIdx) >= maxSessionAffinityEntries {
		back := as.affinityLRU.Back()
		if back == nil {
			break
		}
		be := back.Value.(*affinityLRUEntry)
		as.affinityLRU.Remove(back)
		delete(as.affinityIdx, be.key)
	}

	entry := &affinityLRUEntry{key: key, email: email, expiresAt: now.Add(sessionAffinityTTL)}
	as.affinityIdx[key] = as.affinityLRU.PushFront(entry)
}

// UnstickAccount removes any sticky binding pointing to the exhausted email
func (as *AccountSelector) UnstickAccount(email string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	for k, v := range as.stickyMap {
		if v == email {
			delete(as.stickyMap, k)
			if as.stickyLRU != nil && as.stickyIdx != nil {
				if el, ok := as.stickyIdx[k]; ok {
					as.stickyLRU.Remove(el)
					delete(as.stickyIdx, k)
				}
			}
		}
	}
	for el := as.affinityLRU.Front(); el != nil; {
		next := el.Next()
		entry := el.Value.(*affinityLRUEntry)
		if entry.email == email {
			as.affinityLRU.Remove(el)
			delete(as.affinityIdx, entry.key)
		}
		el = next
	}
}
