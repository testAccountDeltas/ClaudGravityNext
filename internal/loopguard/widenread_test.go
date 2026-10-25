package loopguard

import (
	"testing"

	"github.com/tidwall/gjson"
)

func nudged(string) *LoopGuardManager { return newTestManager() }

func TestWidenPagedRead_NeedsNoNudgeAndOnlyChangesLimit(t *testing.T) {
	const key = "main|s|i"
	args := `{"file_path":"/x/a.md","offset":100,"limit":100}`

	got, ok := nudged(key).WidenPagedRead(key, "Read", args)
	if !ok || gjson.Get(got, "limit").Int() != WidenedReadLimit {
		t.Fatalf("limit not widened: %s", got)
	}
	if gjson.Get(got, "offset").Int() != 100 || gjson.Get(got, "file_path").String() != "/x/a.md" {
		t.Fatalf("only limit may change: %s", got)
	}
}

func TestWidenPagedRead_LeavesEverythingElseAlone(t *testing.T) {
	const key = "main|s|i"
	for name, c := range map[string]struct{ tool, args string }{
		"another tool":      {"Grep", `{"file_path":"/a","limit":10}`},
		"no limit":          {"Read", `{"file_path":"/a"}`},
		"already large":     {"Read", `{"file_path":"/a","limit":800}`},
		"much larger":       {"Read", `{"file_path":"/a","limit":2000}`},
		"zero limit":        {"Read", `{"file_path":"/a","limit":0}`},
		"limit not numeric": {"Read", `{"file_path":"/a","limit":"100"}`},
		"no path":           {"Read", `{"limit":100}`},
		"not json":          {"Read", `nope`},
	} {
		if got, ok := nudged(key).WidenPagedRead(key, c.tool, c.args); ok || got != c.args {
			t.Errorf("%s: changed to %s", name, got)
		}
	}
	if _, ok := nudged("").WidenPagedRead("", "Read", `{"file_path":"/a","limit":10}`); !ok {
		t.Error("an empty agent key still widens")
	}
	if _, ok := nudged(key).WidenPagedRead(key, "read", `{"file_path":"/a","limit":10}`); !ok {
		t.Error("tool name matching is case-insensitive")
	}
}

// Walking a big file in 100-line slices: each slice is widened, but a retry
// inside the range already granted (Read refused the big slice, or the model
// re-reads lines it was given) is passed through untouched.
func TestWidenPagedRead_SequentialPagingAndRetries(t *testing.T) {
	const key = "main|s|i"
	lg := nudged(key)
	read := func(path string, off int) (int64, bool) {
		args := `{"file_path":"` + path + `","offset":` + itoa(off) + `,"limit":100}`
		got, ok := lg.WidenPagedRead(key, "Read", args)
		return gjson.Get(got, "limit").Int(), ok
	}
	if l, ok := read("/f", 0); !ok || l != WidenedReadLimit {
		t.Fatalf("first page: %d %v", l, ok)
	}
	if l, ok := read("/f", 0); ok || l != 100 {
		t.Fatalf("a retry of the same slice must keep the model's own limit: %d %v", l, ok)
	}
	if l, ok := read("/f", 100); ok || l != 100 {
		t.Fatalf("re-reading lines inside the widened range stays small: %d %v", l, ok)
	}
	if l, ok := read("/f", WidenedReadLimit); !ok || l != WidenedReadLimit {
		t.Fatalf("the next unseen slice is widened again: %d %v", l, ok)
	}
	if l, ok := read("/other", 0); !ok || l != WidenedReadLimit {
		t.Fatalf("a different file is widened: %d %v", l, ok)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}
