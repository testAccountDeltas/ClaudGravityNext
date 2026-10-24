package editrepair

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Regression: a live agent run lost a file to this. old_string carried one
// tab too many on every line (repaired by fuzzy matching), new_string appended
// new top-level code after the closing "}" of the block. That "}" sits at
// column 0, and the empty anchor indent was treated as "unknown", so every
// appended line lost its own indentation and got one flat indent instead.
func TestRepairKeepsInsertedCodeIndentAfterColumn0Anchor(t *testing.T) {
	const file = "package p\n\nfunc a() {\n\tif x {\n\t\tdo()\n\t}\n}\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "p.go")
	if err := os.WriteFile(path, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	RecordSessionCWD("col0", dir)

	shift := func(s string) string { return "\t" + strings.ReplaceAll(s, "\n", "\n\t") }
	old := shift("\tif x {\n\t\tdo()\n\t}\n}")
	added := "type w struct{}\n\nfunc (w) f() int {\n\tif true {\n\t\treturn 1\n\t}\n\treturn 0\n}"
	args, _ := json.Marshal(map[string]any{"file_path": path, "old_string": old,
		"new_string": old + "\n\n" + added})

	out, changed := RepairEditToolCall("Edit", string(args), "col0")
	if !changed {
		t.Fatal("old_string with a stray extra tab should have been repaired")
	}
	fixedOld := gjson.Get(out, "old_string").String()
	if !strings.Contains(file, fixedOld) {
		t.Fatalf("repaired old_string does not match the file: %q", fixedOld)
	}
	got := gjson.Get(out, "new_string").String()
	if want := fixedOld + "\n\n" + added; got != want {
		t.Fatalf("new_string not aligned with the repaired old_string:\n%q\nwant\n%q", got, want)
	}
}

// A column-0 line that old_string over-indented must lose the extra indent in
// new_string too, line 0 included.
func TestRepairStripsOverIndentFromColumn0Line(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte("x := 1\nrest\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	RecordSessionCWD("col0b", dir)
	args, _ := json.Marshal(map[string]any{"file_path": path, "old_string": "\tx := 1",
		"new_string": "\tx := 2\n\ty := 3"})
	out, _ := RepairEditToolCall("Edit", string(args), "col0b")
	if got := gjson.Get(out, "new_string").String(); got != "x := 2\ny := 3" {
		t.Fatalf("new_string = %q, want it flush-left", got)
	}
}
