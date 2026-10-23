// Part of the editrepair package's port from superrelay (v1)'s
// edit_repair.go — see cwd.go for the split rationale.
//
// sessionSnapshotMap tracks, per session, the last content the MODEL
// ITSELF has seen/produced for a given file path via Read/Write/Edit tool
// calls already present in the conversation history sent with this very
// request. Unlike reading the gateway's own disk, this works identically
// whether the gateway and the client share a filesystem or not -- the
// history is already inside the request body.
package editrepair

import (
	"container/list"
	"regexp"
	"strings"
	"sync"

	"claudgravity/internal/ephemeral"
	"claudgravity/internal/pathkey"
)

const (
	maxSessionFiles     = 50
	maxSnapshotFileSize = 1 * 1024 * 1024 // 1MB
)

// snapshotEntry holds both of v1's per-session nested maps
// (sessionSnapMap[sessionID] and sessionSnapFull[sessionID]) in one
// struct, stored as a pointer in the ephemeral.Store below — mutating
// the maps through a Get()'d pointer persists, per
// internal/ephemeral's own TestStore_PointerValueMutationsPersist.
type snapshotEntry struct {
	mu      sync.RWMutex
	content map[string]string        // canonical path -> last known content
	full    map[string]bool          // canonical path -> whether content is complete
	order   *list.List               // front = most recently updated, back = oldest
	nodes   map[string]*list.Element // canonical path -> node in order
}

var (
	sessionSnapshots       = ephemeral.New[*snapshotEntry](sessionSnapStaleAfter, sessionSnapSweepEvery)
	sessionSnapshotsInitMu sync.Mutex
)

func getOrCreateSnapshotEntry(sessionID string) *snapshotEntry {
	if e, ok := sessionSnapshots.Get(sessionID); ok && e != nil {
		return e
	}
	sessionSnapshotsInitMu.Lock()
	defer sessionSnapshotsInitMu.Unlock()
	if e, ok := sessionSnapshots.Get(sessionID); ok && e != nil {
		return e
	}
	e := &snapshotEntry{
		content: make(map[string]string),
		full:    make(map[string]bool),
		order:   list.New(),
		nodes:   make(map[string]*list.Element),
	}
	sessionSnapshots.Set(sessionID, e)
	return e
}

// stripCatNPrefix reverses the Read tool's "cat -n"-style numbering
// ("   123\tcontent") back into raw file text. Lines that don't match the
// pattern (stray warnings etc.) are kept as-is rather than dropped.
var catNLinePrefix = regexp.MustCompile(`^\s*\d+\t`)

func isCatNDump(s string) bool {
	if s == "" {
		return false
	}
	lines := strings.Split(s, "\n")
	matched := 0
	hasLeadingSpaces := 0
	for _, l := range lines {
		if l == "" {
			continue
		}
		if catNLinePrefix.MatchString(l) {
			matched++
			trimmed := strings.TrimLeft(l, " ")
			if len(l)-len(trimmed) >= 2 {
				hasLeadingSpaces++
			}
		}
	}
	if matched >= 2 && hasLeadingSpaces >= 2 {
		return true
	}
	if matched == 1 && len(lines) == 1 && hasLeadingSpaces >= 1 {
		return true
	}
	return false
}

func stripCatNPrefix(dump string) string {
	lines := strings.Split(dump, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if loc := catNLinePrefix.FindStringIndex(l); loc != nil {
			out = append(out, l[loc[1]:])
		} else {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// canonicalSnapshotKey normalizes a tool-supplied file path before it's used
// as a map key — delegates to internal/pathkey, the single owner of this
// normalization (internal/sanitize needs the identical rule for its own
// read-dedup cache; see that package for why it's not a second copy).
func canonicalSnapshotKey(path string) string {
	return pathkey.Canonical(path)
}

func lookupSessionSnapshotKey(sessionID, key string) (string, bool) {
	if e, ok := sessionSnapshots.Get(sessionID); ok && e != nil {
		e.mu.RLock()
		v, ok := e.content[key]
		e.mu.RUnlock()
		if ok {
			return v, true
		}
	}
	// Same parent-session fallback as GetSessionCWD, for the same reason:
	// a subagent's own first edit call has no snapshots of its own yet,
	// but the parent session it was spawned from (and read the same file
	// in) already does.
	if parent, ok := parentSessionKey(sessionID); ok {
		if e, ok := sessionSnapshots.Get(parent); ok && e != nil {
			e.mu.RLock()
			v, ok := e.content[key]
			e.mu.RUnlock()
			return v, ok
		}
	}
	return "", false
}

func getSessionSnapshot(sessionID, path string) (string, bool) {
	key := canonicalSnapshotKey(path)
	if v, ok := lookupSessionSnapshotKey(sessionID, key); ok {
		return v, true
	}
	if !isAbsPath(path) {
		if cwd := GetSessionCWD(sessionID); cwd != "" {
			if v, ok := lookupSessionSnapshotKey(sessionID, canonicalSnapshotKey(joinCWD(cwd, path))); ok {
				return v, true
			}
		}
	}
	return "", false
}

func lookupSessionSnapshotFull(sessionID, key string) (bool, bool) {
	if e, ok := sessionSnapshots.Get(sessionID); ok && e != nil {
		e.mu.RLock()
		full, ok := e.full[key]
		e.mu.RUnlock()
		if ok {
			return full, true
		}
	}
	// A Round 15 audit caught that getSessionSnapshot falls back to the
	// parent session (just above) but this function never did — a
	// subagent's first partial read of a file the PARENT session already
	// read in full got isSessionSnapshotFull==false (no entry at all),
	// mismatched the parent's true "full" state, and let a later small
	// partial read overwrite/destroy the inherited full snapshot instead
	// of being treated as a refinement of already-complete data.
	if parent, ok := parentSessionKey(sessionID); ok {
		if e, ok := sessionSnapshots.Get(parent); ok && e != nil {
			e.mu.RLock()
			full, ok := e.full[key]
			e.mu.RUnlock()
			return full, ok
		}
	}
	return false, false
}

// isSessionSnapshotFull reports whether the currently stored snapshot for
// path is a complete file (see snapshotEntry.full's doc comment). False
// for both "no snapshot at all" and "a partial one" — callers that need
// to distinguish those two already have getSessionSnapshot's own ok bool.
func isSessionSnapshotFull(sessionID, path string) bool {
	key := canonicalSnapshotKey(path)
	if full, ok := lookupSessionSnapshotFull(sessionID, key); ok {
		return full
	}
	if !isAbsPath(path) {
		if cwd := GetSessionCWD(sessionID); cwd != "" {
			if full, ok := lookupSessionSnapshotFull(sessionID, canonicalSnapshotKey(joinCWD(cwd, path))); ok {
				return full
			}
		}
	}
	return false
}

func setSessionSnapshot(sessionID, path, content string, isFull bool) {
	if sessionID == "" || path == "" {
		return
	}
	// Always canonicalize to absolute path when session CWD is known.
	if !isAbsPath(path) {
		if cwd := GetSessionCWD(sessionID); cwd != "" {
			path = joinCWD(cwd, path)
		}
	}
	key := canonicalSnapshotKey(path)

	// Ceiling: if content exceeds 1MB, do not store to prevent memory bloat.
	if len(content) > maxSnapshotFileSize {
		if e, ok := sessionSnapshots.Get(sessionID); ok && e != nil {
			e.mu.Lock()
			delete(e.content, key)
			delete(e.full, key)
			if el, exists := e.nodes[key]; exists {
				e.order.Remove(el)
				delete(e.nodes, key)
			}
			e.mu.Unlock()
		}
		return
	}

	e := getOrCreateSnapshotEntry(sessionID)
	e.mu.Lock()
	if e.order == nil {
		e.order = list.New()
		e.nodes = make(map[string]*list.Element)
	}
	if e.content == nil {
		e.content = make(map[string]string)
	}
	if e.full == nil {
		e.full = make(map[string]bool)
	}

	if el, exists := e.nodes[key]; exists {
		e.order.MoveToFront(el)
	} else {
		e.nodes[key] = e.order.PushFront(key)
	}
	e.content[key] = content
	e.full[key] = isFull

	// Evict oldest file entries when exceeding per-session file ceiling.
	for e.order.Len() > maxSessionFiles {
		oldest := e.order.Back()
		if oldest == nil {
			break
		}
		oldKey := oldest.Value.(string)
		e.order.Remove(oldest)
		delete(e.nodes, oldKey)
		delete(e.content, oldKey)
		delete(e.full, oldKey)
	}
	e.mu.Unlock()
	sessionSnapshots.Set(sessionID, e) // also refreshes this session's TTL
}
