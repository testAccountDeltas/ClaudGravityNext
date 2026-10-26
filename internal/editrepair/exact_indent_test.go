package editrepair

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// When old_string already matches the file byte for byte, the region it names
// is, by definition, in the file's own style. A replacement that continues
// that region (a help text, SQL or YAML inside a raw string of a tab-indented
// Go file, which really is indented with spaces) must reach the file as the
// model wrote it. Converting its spaces to tabs corrupted such files.
func TestExactMatch_KeepsDeliberateSpaceIndentInsideARawString(t *testing.T) {
	const session = "exact-indent-session"
	file := "func usage() string {\n\treturn `\n    -h  help\n`\n}\n"
	setSessionSnapshot(session, "usage.go", file, true)

	args := `{"file_path":"usage.go","old_string":"    -h  help","new_string":"    -h  help\n    -v  verbose"}`
	out, changed := RepairEditToolCall("Edit", args, session)
	got := gjson.Get(out, "new_string").String()
	if got != "    -h  help\n    -v  verbose" {
		t.Fatalf("new_string was altered (changed=%v): %q", changed, got)
	}
}

// The repair this branch exists for still works: the file is tab-indented,
// old_string matches on a line without indentation, and the model indented the
// new block with spaces.
func TestExactMatch_StillConvertsSpaceIndentedCodeInATabFile(t *testing.T) {
	const session = "exact-indent-session-2"
	file := "func f() {\n\tx := 1\n\t_ = x\n}\n"
	setSessionSnapshot(session, "f.go", file, true)

	args := `{"file_path":"f.go","old_string":"x := 1","new_string":"x := 1\n    y := 2"}`
	out, _ := RepairEditToolCall("Edit", args, session)
	got := gjson.Get(out, "new_string").String()
	if !strings.Contains(got, "\ty := 2") {
		t.Fatalf("the space-indented block should be converted to the file's tabs: %q", got)
	}
}
