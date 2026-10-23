package editrepair

import (
	"strings"
	"testing"
)

// TestGAP06_PreservesIntentionalDedentAndReindent tests that when the model's
// old_string matched the file's indentation (fileIndent == oldIndent),
// reapplyLineIndents does NOT force new_string's lines back to the file's
// pre-edit indentation, allowing intentional de-dentation or re-indentation.
func TestGAP06_PreservesIntentionalDedentAndReindent(t *testing.T) {
	sessionID := "gap06-session"
	// File has a nested block at 8 spaces
	fileContent := "def foo():\n        if condition:\n            do_something()\n            do_more()\n"
	setSessionSnapshot(sessionID, "gap06.py", fileContent, true)

	// Model wants to de-dent the two inner lines from 12 spaces to 8 spaces (removing if condition)
	// old_string matches line 1 and 2 of if body, with fuzzy quotes or dash in other parts or exact
	// Here we simulate a fuzzy match where old_string indentation matches the file (12 spaces)
	oldStr := "            do_something()\n            do_more()"
	newStr := "        do_something()\n        do_more()"

	// 1. Test findFuzzyLineMatch directly: lineIndents should be empty strings because fileIndent == oldIndent
	repairedExact, lineIndents, fileLine0Indent, ok := findFuzzyLineMatch(fileContent, oldStr)
	if !ok {
		t.Fatalf("findFuzzyLineMatch failed to match exact indentation lines")
	}
	if len(lineIndents) != 2 {
		t.Fatalf("expected 2 lineIndents entries, got %d", len(lineIndents))
	}
	// For each line where oldIndent == fileIndent, lineIndents[j] must be ""
	if lineIndents[0] != "" || lineIndents[1] != "" {
		t.Fatalf("expected lineIndents to be empty strings to preserve model intent, got %v", lineIndents)
	}

	// 2. Test reapplyLineIndents: must not override the 8 spaces in newStr back to 12 spaces
	reindented, changed := reapplyLineIndents(newStr, lineIndents, fileLine0Indent, oldStr)
	if changed && reindented != newStr {
		t.Fatalf("reapplyLineIndents modified intentional dedent:\ngot:  %q\nwant: %q", reindented, newStr)
	}

	// 3. Test through RepairEditToolCall end-to-end
	// To trigger fuzzy repair, old_string has non-breaking space or typographical quote
	oldStrFuzzy := "            do_something() \n            do_more()"
	args := `{"file_path":"gap06.py","old_string":"` + strings.ReplaceAll(oldStrFuzzy, "\n", "\\n") + `","new_string":"` + strings.ReplaceAll(newStr, "\n", "\\n") + `"}`
	repairedArgs, didRepair := RepairEditToolCall("Edit", args, sessionID)
	if !didRepair {
		t.Fatalf("expected RepairEditToolCall to repair fuzzy old_string")
	}
	// Repaired old_string should match file, and new_string must KEEP the 8-space dedent
	if !strings.Contains(repairedArgs, `"new_string":"        do_something()\n        do_more()"`) {
		t.Fatalf("RepairEditToolCall overwrote intentional dedent: %s", repairedArgs)
	}
	_ = repairedExact
}

// TestFuzzyMatching_CRLF_vs_LF tests line ending normalization and fuzzy repair
// across CRLF and LF files.
func TestFuzzyMatching_CRLF_vs_LF(t *testing.T) {
	sessionID := "crlf-lf-session"

	// 1. File is CRLF, model sends LF old_string and new_string
	crlfFile := "function test() {\r\n    let a = 1;\r\n    let b = 2;\r\n}\r\n"
	setSessionSnapshot(sessionID, "crlf.js", crlfFile, true)

	argsLF := `{"file_path":"crlf.js","old_string":"    let a = 1;\n    let b = 2;","new_string":"    let a = 10;\n    let b = 20;"}`
	repaired, changed := RepairEditToolCall("Edit", argsLF, sessionID)
	if !changed {
		t.Fatalf("expected CRLF normalization to trigger repair")
	}
	if !strings.Contains(repaired, "let a = 1;\\r\\n    let b = 2;") {
		t.Fatalf("expected old_string to be converted to CRLF: %s", repaired)
	}
	if !strings.Contains(repaired, "let a = 10;\\r\\n    let b = 20;") {
		t.Fatalf("expected new_string to match CRLF endings: %s", repaired)
	}

	// 2. File is LF, model sends CRLF old_string and new_string
	lfFile := "function test() {\n    let a = 1;\n    let b = 2;\n}\n"
	setSessionSnapshot(sessionID, "lf.js", lfFile, true)

	argsCRLF := `{"file_path":"lf.js","old_string":"    let a = 1;\r\n    let b = 2;","new_string":"    let a = 10;\r\n    let b = 20;"}`
	repairedLF, changedLF := RepairEditToolCall("Edit", argsCRLF, sessionID)
	if !changedLF {
		t.Fatalf("expected LF normalization to trigger repair")
	}
	if strings.Contains(repairedLF, "\\r\\n") {
		t.Fatalf("expected CRLF to be stripped for LF file: %s", repairedLF)
	}
	if !strings.Contains(repairedLF, "let a = 1;\\n    let b = 2;") {
		t.Fatalf("expected old_string to have LF endings: %s", repairedLF)
	}
}

// TestMultiLineExpandsToSingleLine tests what happens when a multi-line old_string
// is replaced by a single-line new_string.
func TestMultiLineExpandsToSingleLine(t *testing.T) {
	sessionID := "multi-to-single"
	fileContent := "class Handler:\n    def handle(self):\n        val = 1\n        val += 2\n        return val\n"
	setSessionSnapshot(sessionID, "handler.py", fileContent, true)

	// Multi-line replacement (3 lines collapsed into 1 line) with a non-breaking space
	newStr := "        return 3"
	oldStrFuzzy := "        val = 1 \n        val += 2\n        return val"

	args := `{"file_path":"handler.py","old_string":"` + strings.ReplaceAll(oldStrFuzzy, "\n", "\\n") + `","new_string":"` + newStr + `"}`
	repaired, changed := RepairEditToolCall("Edit", args, sessionID)
	if !changed {
		t.Fatalf("expected fuzzy repair on multi-to-single line")
	}
	// Verify that the single line new_string retained its proper indentation
	if !strings.Contains(repaired, `"new_string":"        return 3"`) {
		t.Fatalf("expected properly indented single line: %s", repaired)
	}
	// Verify that old_string was repaired to exact file content
	if !strings.Contains(repaired, `"old_string":"        val = 1\n        val += 2\n        return val"`) {
		t.Fatalf("expected multi-line old_string repaired: %s", repaired)
	}
}

// TestSingleLineExpandsToMultiLine tests what happens when a single-line old_string
// expands into multiple lines in new_string.
func TestSingleLineExpandsToMultiLine(t *testing.T) {
	sessionID := "single-to-multi"
	// File has 4 spaces and multiple spaces or tabs
	fileContent := "def calculate():\n\treturn 42\n"
	setSessionSnapshot(sessionID, "calc.py", fileContent, true)

	// Single line fuzzy old_string with spaces instead of file's tab
	oldStr := "    return 42"
	// Model provides multi-line new_string with relative indentation
	newStr := "    x = 10\n    y = 32\n    return x + y"

	args := `{"file_path":"calc.py","old_string":"` + oldStr + `","new_string":"` + strings.ReplaceAll(newStr, "\n", "\\n") + `"}`
	repaired, changed := RepairEditToolCall("Edit", args, sessionID)
	if !changed {
		t.Fatalf("expected single-to-multi repair")
	}
	// The new lines must receive the file's line 0 indent (tab)
	if !strings.Contains(repaired, "\\tx = 10\\n\\ty = 32\\n\\treturn x + y") {
		t.Fatalf("expected tab indent on all expanded lines: %s", repaired)
	}
}

// TestOldStringMultipleOccurrencesRefusesFuzzyRepair tests that when old_string occurs
// multiple times in a file, fuzzy matching safely REFUSES repair to prevent ambiguous replacement.
func TestOldStringMultipleOccurrencesRefusesFuzzyRepair(t *testing.T) {
	sessionID := "ambiguous-session"
	// File contains two identical functions
	fileContent := "func a() {\n    return 0\n}\n\nfunc b() {\n    return 0\n}\n"
	setSessionSnapshot(sessionID, "main.go", fileContent, true)

	// Model wrote "return 0" without 4 spaces indent (fuzzy candidate)
	// Since "return 0" exists at lines 2 and 6, matchIndices has 2 entries
	repairedExact, _, _, ok := findFuzzyLineMatch(fileContent, "return 0")
	if ok {
		t.Fatalf("findFuzzyLineMatch must return ok=false when multiple occurrences match, got: %q", repairedExact)
	}

	args := `{"file_path":"main.go","old_string":"return 0","new_string":"return 1"}`
	_, changed := RepairEditToolCall("Edit", args, sessionID)
	if changed {
		t.Fatalf("RepairEditToolCall must NOT repair when match is ambiguous")
	}
}

// TestStripCatNPrefix_EdgeCases tests line number stripping with various prefixes,
// single-line vs multi-line, and non-cat-n lines.
func TestStripCatNPrefix_EdgeCases(t *testing.T) {
	// Standard cat -n output from Read tool (6-character right-aligned line numbers + tab)
	dump1 := "     1\tfirst line\n     2\tsecond line\n    10\ttenth line\n   100\thundredth line"
	got1 := stripCatNPrefix(dump1)
	expected1 := "first line\nsecond line\ntenth line\nhundredth line"
	if got1 != expected1 {
		t.Fatalf("stripCatNPrefix mismatch:\ngot:  %q\nwant: %q", got1, expected1)
	}

	// Mixed output with a warning message or empty line (no line number)
	dump2 := "     1\tline one\nWarning: truncated output\n     3\tline three\n"
	got2 := stripCatNPrefix(dump2)
	expected2 := "line one\nWarning: truncated output\nline three\n"
	if got2 != expected2 {
		t.Fatalf("stripCatNPrefix failed to preserve non-numbered line:\ngot:  %q\nwant: %q", got2, expected2)
	}

	// isCatNDump verification: TSV vs genuine cat -n dump
	tsvLine := "1\tcol1\tcol2\n2\tcol1\tcol2" // no leading spaces before digits
	if isCatNDump(tsvLine) {
		t.Fatalf("isCatNDump must return false for TSV data without leading spaces")
	}
	catNDump := "     1\tfunc foo()\n     2\t\treturn"
	if !isCatNDump(catNDump) {
		t.Fatalf("isCatNDump must return true for genuine Read dump with leading spaces")
	}
}

func TestAdversarial_NonExistentFileWithSnapshot(t *testing.T) {
	sessionID := "nonexistent-file-session"
	snapshotContent := "package main\n\nfunc Hello() string {\n\treturn \"hello\"\n}\n"
	setSessionSnapshot(sessionID, "does/not/exist/on/disk.go", snapshotContent, true)

	// Model sends edit with 4 spaces instead of tab
	args := `{"file_path":"does/not/exist/on/disk.go","old_string":"    return \"hello\"","new_string":"    return \"world\""}`
	repaired, changed := RepairEditToolCall("Edit", args, sessionID)
	if !changed {
		t.Fatalf("expected repair against session snapshot for non-existent file, got changed=false")
	}
	t.Logf("Repaired args from snapshot: %s", repaired)
}

func TestAdversarial_FuzzyMatchLargeFileComplexity(t *testing.T) {
	// Construct 10,000 lines of repetitive code
	var sb strings.Builder
	for i := 0; i < 10000; i++ {
		sb.WriteString("    // line comment\n")
	}
	sb.WriteString("    target_unique_variable = 42\n")
	for i := 0; i < 5000; i++ {
		sb.WriteString("    // line comment\n")
	}
	content := sb.String()

	// 1. Unique match
	repairedExact, _, _, ok := findFuzzyLineMatch(content, "target_unique_variable = 42")
	if !ok || repairedExact != "target_unique_variable = 42" {
		t.Fatalf("failed unique match on large file: ok=%v, got=%q", ok, repairedExact)
	}

	// 2. Multi-line match with repetitive prefixes (worst case scenario)
	var oldRepetitive strings.Builder
	for i := 0; i < 500; i++ {
		oldRepetitive.WriteString("    // line comment\n")
	}
	oldRepetitive.WriteString("    target_unique_variable = 42")

	repairedExact2, _, _, ok2 := findFuzzyLineMatch(content, oldRepetitive.String())
	if !ok2 {
		t.Fatalf("failed repetitive multi-line match: ok=%v", ok2)
	}
	_ = repairedExact2
}

