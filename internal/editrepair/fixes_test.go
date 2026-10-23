package editrepair

import (
	"fmt"
	"strings"
	"testing"
)

func TestMergeSnapshotWithSlice_TrailingNewline_DoesNotDeleteFollowingLine(t *testing.T) {
	existing := "line 1\nline 2\nline 3\nline 4\nline 5"
	readOutputWithTrailingNewline := "     1\tLINE 1\n     2\tLINE 2\n"

	merged := mergeSnapshotWithSlice(existing, readOutputWithTrailingNewline)
	expected := "LINE 1\nLINE 2\nline 3\nline 4\nline 5"
	if merged != expected {
		t.Fatalf("merged = %q, want %q", merged, expected)
	}
}

func TestReapplyLineIndents_EmptyNewString_ReturnsEmpty(t *testing.T) {
	newStr := ""
	lineIndents := []string{"    "}
	fileLine0Indent := "    "
	oldStr := "    someCode()"

	result, changed := reapplyLineIndents(newStr, lineIndents, fileLine0Indent, oldStr)
	if result != "" || changed {
		t.Fatalf("reapplyLineIndents = %q (changed: %v), want empty string with changed=false", result, changed)
	}
}

func TestRepairEditToolCall_TSVLine_NotStrippedOnExactMatch(t *testing.T) {
	sessionID := "sess-tsv"
	setSessionSnapshot(sessionID, "data.tsv", "header1\theader2\n1\tAlice\n2\tBob", true)

	// Model sends exact match on old_string and adds a TSV line in new_string starting with a digit and tab
	args := `{"file_path":"data.tsv","old_string":"1\tAlice","new_string":"1\tAlice\n3\tCharlie"}`
	repaired, changed := RepairEditToolCall("edit", args, sessionID)
	if changed {
		// Even if arguments are rewritten or unchanged, new_string must NOT strip 3\t
		if strings.Contains(repaired, "Charlie") && !strings.Contains(repaired, "3\tCharlie") {
			t.Fatalf("new_string stripped TSV column: %s", repaired)
		}
	}
}

func TestRecordSessionFileSnapshots_LFNotConvertedToCRLF(t *testing.T) {
	sessionID := "sess-crlf-guard"
	filePath := "test_lf.txt"
	setSessionSnapshot(sessionID, filePath, "line1\nline2\nline3\n", true)

	// Simulate an edit with CRLF in old_string/new_string
	// The file snapshot should remain LF
	body := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"edit","input":{"file_path":"test_lf.txt","old_string":"line2\r\n","new_string":"LINE2\r\n"}}]}]}`
	RecordSessionFileSnapshots(sessionID, []byte(body))

	snap, ok := getSessionSnapshot(sessionID, filePath)
	if !ok {
		t.Fatalf("snapshot not found")
	}
	if strings.Contains(snap, "\r\n") {
		t.Fatalf("LF snapshot was converted to CRLF: %q", snap)
	}
	if !strings.Contains(snap, "LINE2\n") {
		t.Fatalf("expected updated content LINE2\n in snapshot, got: %q", snap)
	}
}

func TestSessionSnapshot_FileCountCeiling_EvictsOldest(t *testing.T) {
	sessionID := "sess-ceiling-test"
	for i := 0; i < 55; i++ {
		path := strings.Repeat("a", 1) + string(rune('0'+i%10)) + "-" + strings.Repeat("x", i) + ".go"
		setSessionSnapshot(sessionID, path, "content", true)
	}
	e, ok := sessionSnapshots.Get(sessionID)
	if !ok || e == nil {
		t.Fatalf("expected session snapshot entry to exist")
	}
	e.mu.RLock()
	count := len(e.content)
	orderLen := e.order.Len()
	e.mu.RUnlock()
	if count != maxSessionFiles {
		t.Fatalf("expected %d files, got %d", maxSessionFiles, count)
	}
	if orderLen != maxSessionFiles {
		t.Fatalf("expected order list len %d, got %d", maxSessionFiles, orderLen)
	}
}

func TestSessionSnapshot_MaxFileSizeCeiling(t *testing.T) {
	sessionID := "sess-filesize-test"
	largeContent := strings.Repeat("X", maxSnapshotFileSize+10)
	setSessionSnapshot(sessionID, "/large.go", largeContent, true)
	if _, ok := getSessionSnapshot(sessionID, "/large.go"); ok {
		t.Fatalf("expected file exceeding maxSnapshotFileSize to not be stored")
	}

	smallContent := "small content"
	setSessionSnapshot(sessionID, "/small.go", smallContent, true)
	if got, ok := getSessionSnapshot(sessionID, "/small.go"); !ok || got != smallContent {
		t.Fatalf("expected small file to be stored, got %q (ok=%v)", got, ok)
	}
}

func TestSessionSnapshot_CanonicalAbsolutePath(t *testing.T) {
	sessionID := "sess-canon-test"
	cwd := "/workspace/myproject"
	RecordSessionCWD(sessionID, cwd)

	relPath := "src/utils.go"
	content := "package utils"
	setSessionSnapshot(sessionID, relPath, content, true)

	// Must be accessible via relative path and absolute path
	if got, ok := getSessionSnapshot(sessionID, relPath); !ok || got != content {
		t.Fatalf("failed to retrieve via relative path: got %q (ok=%v)", got, ok)
	}
	absPath := joinCWD(cwd, relPath)
	if got, ok := getSessionSnapshot(sessionID, absPath); !ok || got != content {
		t.Fatalf("failed to retrieve via absolute path: got %q (ok=%v)", got, ok)
	}
}

func TestSessionSnapshot_ConcurrentSet_NoRace(t *testing.T) {
	sessionID := "sess-race-test"
	done := make(chan bool)
	for i := 0; i < 30; i++ {
		go func(idx int) {
			path := "/file" + string(rune('a'+idx%26)) + ".go"
			setSessionSnapshot(sessionID, path, "content", true)
			_, _ = getSessionSnapshot(sessionID, path)
			done <- true
		}(i)
	}
	for i := 0; i < 30; i++ {
		<-done
	}
}

func TestRecordSessionFileSnapshots_MergeSliceBeyond2000Lines(t *testing.T) {
	sessionID := "sess-large-file-merge"
	filePath := "/workspace/huge_file.go"

	// Turn 1: Initial read of lines 1 to 2000 (cat -n formatted)
	var sb1 strings.Builder
	for i := 1; i <= 2000; i++ {
		sb1.WriteString(fmt.Sprintf("%6d\tline %d\n", i, i))
	}
	bodyTurn1 := fmt.Sprintf(`{
		"messages":[
			{
				"role":"assistant",
				"content":[{"type":"tool_use","id":"call_read_1","name":"read","input":{"file_path":"%s"}}]
			},
			{
				"role":"user",
				"content":[{"type":"tool_result","tool_use_id":"call_read_1","content":%q}]
			}
		]
	}`, filePath, sb1.String())
	RecordSessionFileSnapshots(sessionID, []byte(bodyTurn1))

	snap1, ok1 := getSessionSnapshot(sessionID, filePath)
	if !ok1 {
		t.Fatalf("expected snapshot to exist after turn 1")
	}
	if !strings.Contains(snap1, "line 1") || !strings.Contains(snap1, "line 2000") {
		t.Fatalf("turn 1 snapshot missing boundary lines")
	}

	// Turn 2: Subsequent read of lines 2001 to 2500 with offset
	var sb2 strings.Builder
	for i := 2001; i <= 2500; i++ {
		sb2.WriteString(fmt.Sprintf("%6d\tline %d\n", i, i))
	}
	bodyTurn2 := fmt.Sprintf(`{
		"messages":[
			{
				"role":"assistant",
				"content":[{"type":"tool_use","id":"call_read_2","name":"read","input":{"file_path":"%s","offset":2001,"limit":500}}]
			},
			{
				"role":"user",
				"content":[{"type":"tool_result","tool_use_id":"call_read_2","content":%q}]
			}
		]
	}`, filePath, sb2.String())
	RecordSessionFileSnapshots(sessionID, []byte(bodyTurn2))

	snap2, ok2 := getSessionSnapshot(sessionID, filePath)
	if !ok2 {
		t.Fatalf("expected snapshot to exist after turn 2")
	}

	// Crucial check: lines from Turn 1 (lines 1..2000) AND Turn 2 (lines 2001..2500)
	// must BOTH be preserved!
	if !strings.Contains(snap2, "line 1\n") {
		t.Fatalf("turn 1 lines were overwritten by slice! 'line 1' missing from snapshot")
	}
	if !strings.Contains(snap2, "line 2000\n") {
		t.Fatalf("turn 1 end was overwritten by slice! 'line 2000' missing from snapshot")
	}
	if !strings.Contains(snap2, "line 2001\n") {
		t.Fatalf("slice line 2001 not merged into snapshot")
	}
	if !strings.Contains(snap2, "line 2500") {
		t.Fatalf("slice line 2500 not merged into snapshot")
	}

	lines := strings.Split(snap2, "\n")
	if len(lines) < 2500 {
		t.Fatalf("expected at least 2500 lines in snapshot, got %d", len(lines))
	}
}

func TestRecordSessionFileSnapshots_MergeSliceWithExplicitOffsetNoCatN(t *testing.T) {
	sessionID := "sess-explicit-offset-no-catn"
	filePath := "/workspace/data.txt"

	// Existing snapshot with 10 lines
	initial := "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10"
	setSessionSnapshot(sessionID, filePath, initial, false)

	// Sliced read with offset 4 (no cat -n prefixes in tool_result)
	sliceContent := "NEW LINE 4\nNEW LINE 5"
	body := fmt.Sprintf(`{
		"messages":[
			{
				"role":"assistant",
				"content":[{"type":"tool_use","id":"read_call","name":"read","input":{"file_path":"%s","offset":4,"limit":2}}]
			},
			{
				"role":"user",
				"content":[{"type":"tool_result","tool_use_id":"read_call","content":%q}]
			}
		]
	}`, filePath, sliceContent)
	RecordSessionFileSnapshots(sessionID, []byte(body))

	snap, ok := getSessionSnapshot(sessionID, filePath)
	if !ok {
		t.Fatalf("expected snapshot to exist")
	}

	// Verify lines 1-3 preserved, 4-5 updated, 6-10 preserved
	expected := "line 1\nline 2\nline 3\nNEW LINE 4\nNEW LINE 5\nline 6\nline 7\nline 8\nline 9\nline 10"
	if snap != expected {
		t.Fatalf("snapshot mismatch:\ngot:  %q\nwant: %q", snap, expected)
	}
}

func TestRecordSessionFileSnapshots_InitialSliceWithOffsetPadsLines(t *testing.T) {
	sessionID := "sess-initial-slice-offset-pad"
	filePath := "/workspace/padded_file.txt"

	// Turn 1: Initial read with offset 5 (no existing snapshot)
	slice5 := "     5\tline 5\n     6\tline 6"
	body1 := fmt.Sprintf(`{
			"messages":[
				{
					"role":"assistant",
					"content":[{"type":"tool_use","id":"call_5","name":"read","input":{"file_path":"%s","offset":5,"limit":2}}]
				},
				{
					"role":"user",
					"content":[{"type":"tool_result","tool_use_id":"call_5","content":%q}]
				}
			]
		}`, filePath, slice5)
	RecordSessionFileSnapshots(sessionID, []byte(body1))

	snap1, ok1 := getSessionSnapshot(sessionID, filePath)
	if !ok1 {
		t.Fatalf("expected snapshot after turn 1")
	}
	lines1 := strings.Split(snap1, "\n")
	if len(lines1) < 6 {
		t.Fatalf("expected at least 6 lines in padded snapshot, got %d", len(lines1))
	}
	if lines1[4] != "line 5" || lines1[5] != "line 6" {
		t.Fatalf("expected line 5 and 6 at index 4 and 5, got %q, %q", lines1[4], lines1[5])
	}

	// Turn 2: Subsequent read of lines 1-2
	slice1 := "     1\tline 1\n     2\tline 2"
	body2 := fmt.Sprintf(`{
			"messages":[
				{
					"role":"assistant",
					"content":[{"type":"tool_use","id":"call_1","name":"read","input":{"file_path":"%s","offset":1,"limit":2}}]
				},
				{
					"role":"user",
					"content":[{"type":"tool_result","tool_use_id":"call_1","content":%q}]
				}
			]
		}`, filePath, slice1)
	RecordSessionFileSnapshots(sessionID, []byte(body2))

	snap2, ok2 := getSessionSnapshot(sessionID, filePath)
	if !ok2 {
		t.Fatalf("expected snapshot after turn 2")
	}
	lines2 := strings.Split(snap2, "\n")
	if len(lines2) < 6 {
		t.Fatalf("expected at least 6 lines in merged snapshot, got %d", len(lines2))
	}
	if lines2[0] != "line 1" || lines2[1] != "line 2" {
		t.Fatalf("lines 1-2 missing or corrupted: got %q, %q", lines2[0], lines2[1])
	}
	if lines2[4] != "line 5" || lines2[5] != "line 6" {
		t.Fatalf("lines 5-6 were overwritten: got %q, %q", lines2[4], lines2[5])
	}
}
