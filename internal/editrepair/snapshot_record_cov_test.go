package editrepair

import (
	"fmt"
	"strings"
	"testing"
)

func use(id, name, input string) string {
	return fmt.Sprintf(`{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":%s}]}`, id, name, input)
}

func res(id, content string, isErr bool) string {
	return fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"is_error":%v,"content":%s}]}`, id, isErr, content)
}

func body(msgs ...string) []byte {
	return []byte(`{"messages":[` + strings.Join(msgs, ",") + `]}`)
}

func TestRecordSnapshots_IgnoresBadInput(t *testing.T) {
	RecordSessionFileSnapshots("", body(use("a", "write", `{"file_path":"/f","content":"x"}`)))
	RecordSessionFileSnapshots("cov-bad", []byte(`{"messages":"nope"}`))
	RecordSessionFileSnapshots("cov-bad", []byte(`not json`))
	RecordSessionFileSnapshots("cov-bad", body(
		`{"role":"assistant","content":"just text"}`,
		use("", "write", `{"file_path":"/nid","content":"x"}`),
		use("p", "write", `{"content":"no path"}`),
	))
	if _, ok := getSessionSnapshot("cov-bad", "/nid"); ok {
		t.Fatal("tool_use without id must be ignored")
	}
}

func TestRecordSnapshots_RoleGuards(t *testing.T) {
	// A user message quoting a tool_use, and an assistant message carrying a
	// tool_result, are not real file operations.
	fake := `{"role":"user","content":[{"type":"tool_use","id":"u1","name":"write","input":{"file_path":"/fake","content":"evil"}}]}`
	fakeRes := `{"role":"assistant","content":[{"type":"tool_result","tool_use_id":"r1","content":"evil"}]}`
	RecordSessionFileSnapshots("cov-role", body(use("r1", "read", `{"file_path":"/real"}`), fakeRes, fake))
	if _, ok := getSessionSnapshot("cov-role", "/fake"); ok {
		t.Fatal("user-role tool_use must not record")
	}
	if _, ok := getSessionSnapshot("cov-role", "/real"); ok {
		t.Fatal("assistant-role tool_result must not record")
	}
}

func TestRecordSnapshots_ReadFullAndFailedResults(t *testing.T) {
	sid := "cov-read"
	RecordSessionFileSnapshots(sid, body(
		use("r1", "Read", `{"path":"/a.txt"}`),
		res("r1", `[{"type":"text","text":"     1\thello\n     2\tworld"},"\n"]`, false),
		use("r2", "Read", `{"file":"/b.txt"}`),
		res("r2", `"boom"`, true),
		use("r3", "Read", `{"file_path":"/c.txt"}`),
		res("r3", `""`, false),
		use("r4", "Bash", `{"command":"ls"}`),
		res("r4", `"output"`, false),
	))
	got, ok := getSessionSnapshot(sid, "/a.txt")
	if !ok || got != "hello\nworld\n" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	if !isSessionSnapshotFull(sid, "/a.txt") {
		t.Fatal("whole-file read must be full")
	}
	for _, p := range []string{"/b.txt", "/c.txt"} {
		if _, ok := getSessionSnapshot(sid, p); ok {
			t.Fatalf("%s should not be recorded", p)
		}
	}
}

func TestRecordSnapshots_PartialReadsMerge(t *testing.T) {
	sid := "cov-partial"
	RecordSessionFileSnapshots(sid, body(
		use("w", "Write", `{"file_path":"/p.txt","content":"l1\nl2\nl3\nl4"}`),
		use("r", "Read", `{"file_path":"/p.txt","offset":2,"limit":2}`),
		res("r", `"     2\tL2\n     3\tL3"`, false),
	))
	got, _ := getSessionSnapshot(sid, "/p.txt")
	if got != "l1\nL2\nL3\nl4" {
		t.Fatalf("got %q", got)
	}
	if !isSessionSnapshotFull(sid, "/p.txt") {
		t.Fatal("slice merged into a full snapshot stays full")
	}

	// Offset inferred from the first cat -n line number; no prior snapshot.
	sid2 := "cov-partial2"
	RecordSessionFileSnapshots(sid2, body(
		use("r", "Read", `{"file_path":"/q.txt","limit":2}`),
		res("r", `"     3\tC\n     4\tD"`, false),
	))
	got, _ = getSessionSnapshot(sid2, "/q.txt")
	if got != "\n\nC\nD" {
		t.Fatalf("got %q", got)
	}
	if isSessionSnapshotFull(sid2, "/q.txt") {
		t.Fatal("partial read without prior snapshot is not full")
	}

	// Limit without offset alone makes it partial too.
	sid3 := "cov-partial3"
	RecordSessionFileSnapshots(sid3, body(
		use("r", "Read", `{"file_path":"/z.txt","limit":1}`),
		res("r", `"     1\tfirst"`, false),
	))
	if isSessionSnapshotFull(sid3, "/z.txt") {
		t.Fatal("limit read must be partial")
	}
}

func TestRecordSnapshots_EditReplayAndFailure(t *testing.T) {
	sid := "cov-edit"
	RecordSessionFileSnapshots(sid, body(
		use("w", "Write", `{"file_path":"/e.txt","content":"a b\nc b\n"}`),
		use("e1", "Edit", `{"file_path":"/e.txt","old_string":"b","new_string":"X"}`),
		use("e2", "Edit", `{"file_path":"/e.txt","old_str":"b","new_str":"Y","replace_all":true}`),
		use("e3", "Edit", `{"file_path":"/e.txt","old_string":"a X","new_string":"BAD"}`),
		res("e3", `"string not found"`, true),
		use("e4", "Edit", `{"file_path":"/e.txt","old_string":"zzz","new_string":"nope"}`),
	))
	got, _ := getSessionSnapshot(sid, "/e.txt")
	if got != "a X\nc Y\n" {
		t.Fatalf("got %q", got)
	}
	if !isSessionSnapshotFull(sid, "/e.txt") {
		t.Fatal("edit of full snapshot stays full")
	}
}

func TestRecordSnapshots_EditCRLFPreserved(t *testing.T) {
	sid := "cov-crlf"
	RecordSessionFileSnapshots(sid, body(
		use("w", "Write", `{"file_path":"/w.txt","content":"one\r\ntwo\r\nthree"}`),
		use("e", "Edit", `{"file_path":"/w.txt","old_string":"one\ntwo","new_string":"1\n2"}`),
	))
	got, _ := getSessionSnapshot(sid, "/w.txt")
	if got != "1\r\n2\r\nthree" {
		t.Fatalf("got %q", got)
	}
}

func TestRecordSnapshots_RelativeEditResolvesAgainstCWD(t *testing.T) {
	sid := "cov-rel"
	RecordSessionCWD(sid, "/work")
	RecordSessionFileSnapshots(sid, body(
		use("w", "Write", `{"file_path":"/work/r.go","content":"v1"}`),
		use("e", "Edit", `{"file_path":"r.go","old_string":"v1","new_string":"v2"}`),
		use("r", "Read", `{"file_path":"r.go"}`),
		res("r", `"     1\tv3"`, false),
	))
	if got, _ := getSessionSnapshot(sid, "/work/r.go"); got != "v3" {
		t.Fatalf("got %q", got)
	}
}

func TestSnapshotFull_SubagentInheritsParent(t *testing.T) {
	setSessionSnapshot("cov-par", "/shared.go", "content", true)
	setSessionSnapshot("cov-par2", "/partial.go", "c", false)
	if !isSessionSnapshotFull("cov-par:sub", "/shared.go") {
		t.Fatal("subagent should inherit parent's full flag")
	}
	if isSessionSnapshotFull("cov-par2:sub", "/partial.go") {
		t.Fatal("inherited partial stays partial")
	}
	if isSessionSnapshotFull("cov-par:sub", "/missing.go") {
		t.Fatal("missing -> false")
	}
	if isSessionSnapshotFull("cov-other:sub", "/shared.go") {
		t.Fatal("unrelated parent -> false")
	}
	if v, ok := getSessionSnapshot("cov-par:sub", "/shared.go"); !ok || v != "content" {
		t.Fatalf("content fallback: %q %v", v, ok)
	}
}

func TestSnapshotFull_RelativePathViaCWD(t *testing.T) {
	sid := "cov-fullrel"
	RecordSessionCWD(sid, "/root")
	setSessionSnapshot(sid, "x.go", "abc", true) // stored under /root/x.go
	if !isSessionSnapshotFull(sid, "x.go") {
		t.Fatal("relative lookup should resolve via CWD")
	}
	if v, ok := getSessionSnapshot(sid, "/root/x.go"); !ok || v != "abc" {
		t.Fatalf("got %q %v", v, ok)
	}
}

func TestSetSessionSnapshot_OversizeDropsExistingAndIgnoresEmpty(t *testing.T) {
	sid := "cov-big"
	setSessionSnapshot(sid, "/big.txt", "small", true)
	setSessionSnapshot(sid, "/big.txt", strings.Repeat("x", maxSnapshotFileSize+1), true)
	if _, ok := getSessionSnapshot(sid, "/big.txt"); ok {
		t.Fatal("oversize update must evict the stale snapshot")
	}
	setSessionSnapshot("", "/a", "x", true)
	setSessionSnapshot(sid, "", "x", true)
	// Oversize for a session with no entry must not panic or create one.
	setSessionSnapshot("cov-big-none", "/n", strings.Repeat("x", maxSnapshotFileSize+1), false)
	if _, ok := getSessionSnapshot("cov-big-none", "/n"); ok {
		t.Fatal("oversize must not be stored")
	}
}

func TestSetSessionSnapshot_EvictsOldestBeyondCeiling(t *testing.T) {
	sid := "cov-lru"
	for i := 0; i < maxSessionFiles+5; i++ {
		setSessionSnapshot(sid, fmt.Sprintf("/f%d.txt", i), "c", false)
		if i == maxSessionFiles+2 { // touch f0 late so it survives
			setSessionSnapshot(sid, "/f0.txt", "c0", true)
		}
	}
	if _, ok := getSessionSnapshot(sid, "/f0.txt"); !ok {
		t.Fatal("recently refreshed entry must survive eviction")
	}
	if _, ok := getSessionSnapshot(sid, "/f1.txt"); ok {
		t.Fatal("oldest entry should have been evicted")
	}
	if _, ok := getSessionSnapshot(sid, fmt.Sprintf("/f%d.txt", maxSessionFiles+4)); !ok {
		t.Fatal("newest entry must exist")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "", "x", "y") != "x" || firstNonEmpty() != "" || firstNonEmpty("", "") != "" {
		t.Fatal("firstNonEmpty wrong")
	}
}

func TestMergeSnapshotWithSlice_Edges(t *testing.T) {
	// No line numbers and no offset: the dump is returned stripped.
	if got := mergeSnapshotWithSlice("old", "plain\ntext"); got != "plain\ntext" {
		t.Fatalf("got %q", got)
	}
	// Explicit offset with CRLF preserved.
	if got := mergeSnapshotWithSlice("a\r\nb\r\nc", "B", 2); got != "a\r\nB\r\nc" {
		t.Fatalf("got %q", got)
	}
	// Absurd offset is refused.
	if got := mergeSnapshotWithSlice("keep", "x", 60000); got != "keep" {
		t.Fatalf("got %q", got)
	}
}
