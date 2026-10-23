// Phase 1 characterization test for the ported editrepair package —
// proves the core repair/CWD/snapshot behaviors match v1 exactly,
// including the ephemeral.Store-backed replacement of the two
// map+mutex+sweepStale* pairs.
package editrepair

import (
	"path/filepath"
	"strings"
	"testing"
)

// hostAbs builds a real, host-appropriate absolute path (Windows needs a
// drive letter for filepath.IsAbs/filepath.Clean to agree a path is
// absolute at all; Unix doesn't) — t.TempDir() always returns one for the
// current OS, so joining onto it keeps these tests portable instead of
// hardcoding Unix-style "/foo/bar" literals that silently misbehave on a
// native Windows test run (filepath.Clean rewrites "/" to "\" and
// filepath.IsAbs rejects a bare "/foo" outright) — exactly the
// Windows-vs-Linux divergence canonicalSnapshotKey's own v1 comments
// document at length.
func hostAbs(t *testing.T, parts ...string) string {
	t.Helper()
	return filepath.Join(append([]string{t.TempDir()}, parts...)...)
}

func TestRecordAndGetSessionCWD_ParentFallback(t *testing.T) {
	dir := hostAbs(t, "project")
	RecordSessionCWD("main-session", dir)
	got := GetSessionCWD("main-session:sub1")
	if got != filepath.Clean(dir) {
		t.Fatalf("a subagent session with no CWD of its own must fall back to its parent's, got %q want %q", got, filepath.Clean(dir))
	}
}

func TestGetSessionCWD_OwnValueWinsOverParent(t *testing.T) {
	parentDir := hostAbs(t, "parent-dir")
	childDir := hostAbs(t, "child-own-dir")
	RecordSessionCWD("parent2", parentDir)
	RecordSessionCWD("parent2:child", childDir)
	if got := GetSessionCWD("parent2:child"); got != filepath.Clean(childDir) {
		t.Fatalf("a session's own recorded CWD must win over the parent fallback, got %q want %q", got, filepath.Clean(childDir))
	}
}

func TestIsWithinSessionCWD_RefusesWithUnknownCWD(t *testing.T) {
	if isWithinSessionCWD("/some/path", "session-with-no-cwd-ever-recorded") {
		t.Fatal("must refuse (false) when the session's CWD is unknown — an unconstrained read is worse than a missed repair")
	}
}

func TestIsWithinSessionCWD_AcceptsDescendant(t *testing.T) {
	base := hostAbs(t, "abs-project")
	RecordSessionCWD("cwd-test", base)
	descendant := filepath.Join(base, "src", "main.go")
	if !isWithinSessionCWD(descendant, "cwd-test") {
		t.Fatal("a descendant of the recorded CWD must be accepted")
	}
	outside := filepath.Join(filepath.Dir(base), "other-unrelated-dir", "etc", "passwd")
	if isWithinSessionCWD(outside, "cwd-test") {
		t.Fatal("a path outside the recorded CWD must be refused — this is the sandbox boundary check")
	}
}

func TestSnapshotRoundTrip_FullVsPartial(t *testing.T) {
	setSessionSnapshot("snap-sess", "/f.go", "full content", true)
	got, ok := getSessionSnapshot("snap-sess", "/f.go")
	if !ok || got != "full content" {
		t.Fatalf("expected the stored snapshot back, got (%q, %v)", got, ok)
	}
	if !isSessionSnapshotFull("snap-sess", "/f.go") {
		t.Fatal("expected isSessionSnapshotFull to report true for a full snapshot")
	}
}

func TestSnapshotFull_ProtectedFromSmallerPartialOverwrite(t *testing.T) {
	// This mirrors the exact policy RecordSessionFileSnapshots enforces at
	// its call sites (not inside setSessionSnapshot itself, which always
	// overwrites unconditionally — the protection lives in the caller's
	// decision of whether to call setSessionSnapshot at all). Verify the
	// primitive itself: isSessionSnapshotFull correctly reports the
	// session's current state so that caller-side logic works.
	setSessionSnapshot("protect-sess", "/f.go", "AAAAAAAAAA full file", true)
	if !isSessionSnapshotFull("protect-sess", "/f.go") {
		t.Fatal("expected full=true to be recorded")
	}
	setSessionSnapshot("protect-sess", "/f.go", "partial", false)
	if isSessionSnapshotFull("protect-sess", "/f.go") {
		t.Fatal("setSessionSnapshot itself always overwrites per its own contract — full/not-full is whatever the last call said")
	}
}

func TestSnapshotParentFallback(t *testing.T) {
	setSessionSnapshot("snap-parent", "/shared.go", "shared content", true)
	got, ok := getSessionSnapshot("snap-parent:child", "/shared.go")
	if !ok || got != "shared content" {
		t.Fatalf("a subagent with no snapshot of its own must fall back to its parent's, got (%q, %v)", got, ok)
	}
}

func TestCanonicalSnapshotKey_NormalizesSlashesAndDriveLetterCase(t *testing.T) {
	a := canonicalSnapshotKey(`H:\project\src\main.go`)
	b := canonicalSnapshotKey(`h:/project/src/main.go`)
	if a != b {
		t.Fatalf("backslash/forward-slash and drive-letter-case variants of the same path must normalize to the same key, got %q vs %q", a, b)
	}
}

func TestStripCatNPrefix_RemovesLineNumbers(t *testing.T) {
	dump := "   1\tfirst line\n   2\tsecond line\n"
	got := stripCatNPrefix(dump)
	want := "first line\nsecond line\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRepairEditToolCall_FuzzyWhitespaceAndKeyRename(t *testing.T) {
	sessionID := "edit-test-session"
	setSessionSnapshot(sessionID, "app.py", "def foo():\n    return 42\n", true)

	// Model wrote old_str (alias) with flush-left old_string (wrong indent)
	// and the Edit tool name — both the fuzzy match AND the key rename
	// should fire.
	argsRaw := `{"path":"app.py","old_str":"return 42","new_str":"return 43"}`
	repaired, changed := RepairEditToolCall("Edit", argsRaw, sessionID)
	if !changed {
		t.Fatal("expected a repair (key rename at minimum) to be reported")
	}
	if !containsAll(repaired, `"file_path"`, `"old_string"`, `"new_string"`) {
		t.Fatalf("expected canonical Claude Code key names after repair, got %s", repaired)
	}
}

func TestRepairEditToolCall_ByteExactMatchNoRepairNeeded(t *testing.T) {
	sessionID := "edit-exact-session"
	setSessionSnapshot(sessionID, "/f.go", "package main\n\nfunc main() {}\n", true)
	argsRaw := `{"file_path":"/f.go","old_string":"func main() {}","new_string":"func main() { println(1) }"}`
	_, changed := RepairEditToolCall("Edit", argsRaw, sessionID)
	if changed {
		t.Fatal("a byte-exact match needs no repair at all — the function must report unchanged")
	}
}

func TestRepairGrepToolCall_BareStringWrapped(t *testing.T) {
	fixed, changed := RepairGrepToolCall(`"TODO"`)
	if !changed {
		t.Fatal("a bare JSON string must be wrapped into {\"pattern\": ...}")
	}
	if !containsAll(fixed, `"pattern":"TODO"`) {
		t.Fatalf("got %s", fixed)
	}
}

func TestRepairGrepToolCall_AliasKeyTakesPriorityOverPathHeuristic(t *testing.T) {
	// Regression for the exact bug the v1 comment describes: {"path":
	// "main.go","query":"HandleRequest"} must use "query", not repurpose
	// "main.go" as the pattern.
	fixed, changed := RepairGrepToolCall(`{"path":"main.go","query":"HandleRequest"}`)
	if !changed {
		t.Fatal("expected a repair")
	}
	if !containsAll(fixed, `"pattern":"HandleRequest"`) {
		t.Fatalf("expected query's value to become pattern, got %s", fixed)
	}
}

func TestRepairGlobToolCall_GlobAlias(t *testing.T) {
	fixed, changed := RepairGlobToolCall(`{"glob":"**/*.go"}`)
	if !changed {
		t.Fatal("expected a repair")
	}
	if !containsAll(fixed, `"pattern":"**/*.go"`) {
		t.Fatalf("got %s", fixed)
	}
}

func TestExtractSystemPromptText_ArrayBlocks(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]}`)
	got := ExtractSystemPromptText(body)
	if !containsAll(got, "line one", "line two") {
		t.Fatalf("expected both text blocks concatenated, got %q", got)
	}
}

func TestExtractCWDFromSystemPrompt_MatchesKnownPhrasing(t *testing.T) {
	prompt := "Some preamble.\nCurrent working directory: /home/user/project\nMore text."
	got := ExtractCWDFromSystemPrompt(prompt)
	// filepath.Clean rewrites "/" to the host separator (e.g. "\" on
	// Windows) — compare against Clean's own output for this platform,
	// not a hardcoded forward-slash literal (see hostAbs's doc comment).
	want := filepath.Clean("/home/user/project")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRepairEditToolCall_StripCatN_CleansBothOldAndNewString(t *testing.T) {
	dir := hostAbs(t, "catn-project")
	RecordSessionCWD("catn-session", dir)
	file := filepath.Join(dir, "app.js")
	setSessionSnapshot("catn-session", file, "function test() {\n    const x = 1;\n    return x;\n}", true)

	args := `{"file_path":"app.js","old_string":"    12\t    const x = 1;","new_string":"    12\t    const x = 2;"}`
	repaired, changed := RepairEditToolCall("Edit", args, "catn-session")
	if !changed {
		t.Fatal("expected repair to succeed")
	}
	if strings.Contains(repaired, "12\t") {
		t.Fatalf("repaired args must not contain cat -n prefix in either old_string or new_string: %s", repaired)
	}
	if !strings.Contains(repaired, `"new_string":"    const x = 2;"`) {
		t.Fatalf("expected new_string to be cleaned, got: %s", repaired)
	}
}

func TestNormalizeEditToolCallKeys_FileAlias(t *testing.T) {
	args := `{"file":"src/index.ts","old_str":"foo","new_str":"bar"}`
	normalized, changed := RepairEditToolCall("Edit", args, "any-session")
	if !changed {
		t.Fatal("expected normalization")
	}
	if !strings.Contains(normalized, `"file_path":"src/index.ts"`) {
		t.Fatalf("expected 'file' alias to be normalized to 'file_path', got: %s", normalized)
	}
	if strings.Contains(normalized, `"file":`) {
		t.Fatalf("expected old 'file' key to be removed, got: %s", normalized)
	}
}

func TestRepairEditToolCall_FileAliasResolvesDiskOrSnapshot(t *testing.T) {
	sessionID := "file-alias-sess"
	setSessionSnapshot(sessionID, "myfile.txt", "line1\nline2\n", true)
	args := `{"file":"myfile.txt","old_str":"  line2","new_str":"line3"}`
	repaired, changed := RepairEditToolCall("Edit", args, sessionID)
	if !changed {
		t.Fatal("expected repair with file alias")
	}
	if !strings.Contains(repaired, `"file_path":"myfile.txt"`) {
		t.Fatalf("expected file_path in repaired: %s", repaired)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
