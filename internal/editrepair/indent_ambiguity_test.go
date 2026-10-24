package editrepair

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tidwall/gjson"
)

func repairOn(t *testing.T, sid, file, oldS, newS string) (out string, changed bool) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "f.go")
	if err := os.WriteFile(path, []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	RecordSessionCWD(sid, dir)
	args, _ := json.Marshal(map[string]any{"file_path": path, "old_string": oldS, "new_string": newS})
	return RepairEditToolCall("Edit", string(args), sid)
}

// When the text cannot say how new_string is indented, the call must go back
// unrepaired (Edit then fails and the model retries) instead of landing wrong.
func TestRepairRefusesAmbiguousIndentation(t *testing.T) {
	for _, c := range []struct{ name, file, old, new string }{
		{
			// First line dropped; the new first line is shallower than the
			// preserved indent of the old first line, which no span can express.
			"dropped first line shallower than prefix",
			"func f() {\n\t\tstmt()\n\t}\n}\n",
			"stmt()\n    }", "}\nafter()",
		},
		{
			// One-line old_string quoted without indentation, expanded into
			// indented lines: no second matched line reveals the model's frame.
			"one-line old_string expanded with own indentation",
			"func f() {\n\tx := 1\n}\n",
			"x :=   1", "x := 2\n\t\ty := 3",
		},
	} {
		out, changed := repairOn(t, "amb-"+c.name, c.file, c.old, c.new)
		if changed || gjson.Get(out, "old_string").String() != c.old || gjson.Get(out, "new_string").String() != c.new {
			t.Errorf("%s: expected the call to come back untouched, got changed=%v old=%q new=%q",
				c.name, changed, gjson.Get(out, "old_string").String(), gjson.Get(out, "new_string").String())
		}
	}
}

// Ordinary over-indented quotes are still repaired, not refused.
func TestRepairStillRepairsUnambiguousCases(t *testing.T) {
	file := "func f() {\n\tif x {\n\t\ty()\n\t}\n}\n"
	out, changed := repairOn(t, "amb-ok", file, "\t\tif x {\n\t\t\ty()\n\t\t}", "\t\tif x {\n\t\t\ty()\n\t\t\tz()\n\t\t}")
	if !changed {
		t.Fatal("an over-indented block with a clear frame must still be repaired")
	}
	if got, want := gjson.Get(out, "new_string").String(), "\tif x {\n\t\ty()\n\t\tz()\n\t}"; got != want {
		t.Fatalf("new_string = %q, want %q", got, want)
	}
}
