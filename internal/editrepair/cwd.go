// Package editrepair is a port of superrelay (v1)'s edit_repair.go, split
// across six files by responsibility per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 4.3: cwd.go
// (this file — session working-directory tracking), snapshot.go
// (file-content snapshot tracking), edit.go (RepairEditToolCall and its
// core), fuzzymatch.go (the fuzzy line-matching/indent-reapplication
// engine), grepglob.go (RepairGrepToolCall/RepairGlobToolCall),
// systemprompt.go (system-prompt text/CWD extraction).
//
// Every heuristic's BODY is unchanged from v1 — this is exactly the kind
// of boots-on-the-ground, battle-tested logic Part 2 p.2 of the spec
// says is protected only by regression tests, not by structure. The one
// structural change, per Part 1.5/6.1: v1's two independent
// map+mutex+sweepStale* pairs (sessionCWDMap+sessionCWDSeen here,
// sessionSnapMap+sessionSnapFull+sessionSnapSeen in snapshot.go) are now
// both internal/ephemeral.Store instances — one TTL policy, one lock,
// one tested sweep implementation, instead of two hand-rolled copies.
package editrepair

import (
	"path/filepath"
	"strings"
	"time"

	"claudgravity/internal/ephemeral"
)

// sessionSnapStaleAfter/sessionSnapSweepEvery bound the otherwise-
// unbounded growth of the CWD and snapshot stores: without this, every
// session's working directory and every file content a session has ever
// Read/Written stays in memory for the gateway's entire process lifetime,
// across every session that ever connected.
const (
	sessionSnapStaleAfter = 2 * time.Hour
	sessionSnapSweepEvery = 30 * time.Minute
)

// sessionCWD replaces v1's sessionCWDMap+sessionCWDSeen pair — one
// ephemeral.Store call does what used to be two map writes under one
// mutex plus a second map just to track staleness.
var sessionCWD = ephemeral.New[string](sessionSnapStaleAfter, sessionSnapSweepEvery)

func RecordSessionCWD(sessionID, cwd string) {
	if sessionID == "" || cwd == "" {
		return
	}
	sessionCWD.Set(sessionID, filepath.Clean(strings.TrimSpace(cwd)))
}

// parentSessionKey strips a subagent's ":instanceID" suffix, returning the
// bare parent session key it was derived from (see the orchestrator's
// cwdKey/fileSnapKey construction, once ported: "sessionID" for the main
// thread, "sessionID:instanceID" for a subagent instance). Returns ("",
// false) for a key with no such suffix (nothing to fall back to).
func parentSessionKey(sessionID string) (string, bool) {
	idx := strings.LastIndex(sessionID, ":")
	if idx < 0 {
		return "", false
	}
	return sessionID[:idx], true
}

func GetSessionCWD(sessionID string) string {
	if cwd, ok := sessionCWD.Get(sessionID); ok && cwd != "" {
		return cwd
	}
	// An audit caught that a subagent's OWN first-ever edit call, before
	// it has recorded any CWD of its own, had no fallback at all — the
	// main thread's (or the subagent's own earlier-created parent
	// session's) already-known CWD, recorded under the bare key this
	// subagent's own composite key was literally derived FROM, went
	// completely unused. Safe to fall back to: the parent/child
	// relationship is established by this key's own naming convention,
	// not a cross-tenant guess.
	if parent, ok := parentSessionKey(sessionID); ok {
		cwd, _ := sessionCWD.Get(parent)
		return cwd
	}
	return ""
}

// IsAbsPath exports isAbsPath for internal/sanitize's dedup key
// canonicalization, which needs the identical Windows-drive-letter/UNC
// detection this function already provides (independent of the host
// OS the gateway itself runs on) rather than a second, divergent copy.
func IsAbsPath(p string) bool { return isAbsPath(p) }

// JoinCWD exports joinCWD for the same caller/reason as IsAbsPath above.
func JoinCWD(cwd, path string) string { return joinCWD(cwd, path) }

func isAbsPath(p string) bool {
	return filepath.IsAbs(p) || hasDriveOrUNC(p)
}

// hasDriveOrUNC reports a drive-qualified ("C:\x", "c:/x") or UNC ("\\host\share",
// "//host/share") path, whatever the host OS.
func hasDriveOrUNC(p string) bool {
	if len(p) >= 3 && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		return true
	}
	return len(p) >= 2 && (p[0] == '\\' || p[0] == '/') && p[0] == p[1]
}

func joinCWD(cwd, path string) string {
	winCWD := strings.Contains(cwd, "\\")
	// Against a Windows cwd only a drive-qualified or UNC path is absolute; a
	// rooted "/sub/f.go" is relative to the cwd. filepath.IsAbs calls it
	// absolute on a Unix host, so it cannot decide this alone.
	if winCWD && !hasDriveOrUNC(path) {
		return strings.TrimRight(cwd, "\\/") + "\\" + strings.TrimLeft(path, "\\/")
	}
	if isAbsPath(path) {
		return path
	}
	if winCWD {
		return strings.TrimRight(cwd, "\\/") + "\\" + strings.TrimLeft(path, "\\/")
	}
	return filepath.Join(cwd, path)
}

// isWithinSessionCWD reports whether path is the session's recorded working
// directory or a descendant of it. Returns false (refuse) if the session's
// CWD is unknown, or the file resolution never caught up to produce an
// absolute path — an unconstrained read is worse than a missed repair.
func isWithinSessionCWD(path, sessionID string) bool {
	cwd := GetSessionCWD(sessionID)
	if cwd == "" || !isAbsPath(path) {
		return false
	}
	absCWD, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absCWD, absPath)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}
