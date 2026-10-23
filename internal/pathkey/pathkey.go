// Package pathkey normalizes a tool-supplied file path into a stable map
// key across OS, slash direction, and drive-letter casing. Extracted out
// of internal/editrepair (where it originated as v1's own
// canonicalSnapshotKey, superrelay/edit_repair.go) because internal/sanitize
// needs the exact same normalization for its own read-dedup cache and must
// not duplicate it — a second copy drifting out of sync with this one would
// silently reintroduce the cross-OS/cross-casing cache-miss bugs both were
// written to fix (see Canonical's own comments for the specific incidents).
package pathkey

import (
	"path/filepath"
	"strings"
)

// Canonical normalizes path so that two tool calls referring to the same
// real file ("src/app.ts" vs "src\app.ts" vs "H:\project\src\app.ts" vs
// "h:\project\src\app.ts") produce the same key, independent of which OS
// this binary itself targets.
func Canonical(path string) string {
	// filepath.ToSlash is a no-op on the BUILD target's OS, not the path's
	// origin: on a GOOS=linux build, filepath.Separator is '/', so ToSlash
	// leaves a Windows client's backslashes untouched. Strip them
	// explicitly instead.
	p := strings.ReplaceAll(strings.TrimSpace(path), "\\", "/")
	// Windows drive letters are case-insensitive ("H:\..." == "h:\...")
	// but produced two different keys without this normalization.
	if len(p) >= 2 && p[1] == ':' && p[0] >= 'A' && p[0] <= 'Z' {
		p = strings.ToLower(p[:1]) + p[1:]
	}
	// filepath.Clean itself re-introduces the OS divergence the
	// slash-stripping above exists to prevent: on a GOOS=windows build it
	// rewrites "/" back to "\", diverging from a GOOS=linux build's Clean
	// of the same key. Re-normalize after Clean too.
	return strings.ReplaceAll(filepath.Clean(p), "\\", "/")
}
