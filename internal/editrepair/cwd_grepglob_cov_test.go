package editrepair

import (
	"path/filepath"
	"testing"

	"github.com/tidwall/gjson"
)

func TestRecordSessionCWD_IgnoresEmptyAndCleans(t *testing.T) {
	RecordSessionCWD("", "/x")
	RecordSessionCWD("cov-cwd-empty", "")
	if got := GetSessionCWD("cov-cwd-empty"); got != "" {
		t.Fatalf("empty cwd must not be stored, got %q", got)
	}
	RecordSessionCWD("cov-cwd-clean", "  /a/b/../c/  ")
	if got, want := GetSessionCWD("cov-cwd-clean"), filepath.Clean("/a/c"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestGetSessionCWD_SubagentFallsBackToParent(t *testing.T) {
	RecordSessionCWD("cov-parent", "/proj")
	if got := GetSessionCWD("cov-parent:sub1"); got != filepath.Clean("/proj") {
		t.Fatalf("subagent should inherit, got %q", got)
	}
	if got := GetSessionCWD("cov-unknown:sub1"); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := GetSessionCWD("cov-nocolon-unknown"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestIsAbsPath_AllOSStyles(t *testing.T) {
	yes := []string{`C:\x`, `c:/x`, `Z:\`, `\\server\share`, `//host/x`}
	no := []string{"", "rel/x", `C:`, `C:x`, `\x`}
	for _, p := range yes {
		if !IsAbsPath(p) {
			t.Errorf("IsAbsPath(%q) = false", p)
		}
	}
	for _, p := range no {
		if IsAbsPath(p) {
			t.Errorf("IsAbsPath(%q) = true", p)
		}
	}
}

func TestJoinCWD(t *testing.T) {
	if got := JoinCWD(`C:\proj`, `D:\other\f.go`); got != `D:\other\f.go` {
		t.Fatalf("absolute path must pass through, got %q", got)
	}
	if got := JoinCWD(`C:\proj\`, `/sub/f.go`); got != `C:\proj\sub/f.go` {
		t.Fatalf("windows cwd join got %q", got)
	}
	if got, want := JoinCWD("/proj", "sub/f.go"), filepath.Join("/proj", "sub/f.go"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestIsWithinSessionCWD(t *testing.T) {
	dir := t.TempDir()
	RecordSessionCWD("cov-within", dir)
	inside := filepath.Join(dir, "a", "b.go")
	if !isWithinSessionCWD(inside, "cov-within") {
		t.Error("descendant should be within")
	}
	if !isWithinSessionCWD(dir, "cov-within") {
		t.Error("cwd itself should be within")
	}
	if isWithinSessionCWD(filepath.Join(dir, "..", "elsewhere.go"), "cov-within") {
		t.Error("escape via .. must be refused")
	}
	if isWithinSessionCWD("rel.go", "cov-within") {
		t.Error("relative path must be refused")
	}
	if isWithinSessionCWD(inside, "cov-within-unknown") {
		t.Error("unknown session must be refused")
	}
}

func TestExtractCWDFromSystemPrompt(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"no cwd here", ""},
		{"Primary working directory: /home/u/proj\nother", filepath.Clean("/home/u/proj")},
		{"Current working directory: \"/a/b\"\r\nx", filepath.Clean("/a/b")},
		{"- working directory `/q/r`", filepath.Clean("/q/r")},
	}
	for _, c := range cases {
		if got := ExtractCWDFromSystemPrompt(c.in); got != c.want {
			t.Errorf("Extract(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestRepairGrepToolCall_Table(t *testing.T) {
	cases := []struct {
		name, in string
		changed  bool
		pattern  string
		hasPath  bool
	}{
		{"not json value", ``, false, "", false},
		{"bare string", `"TODO"`, true, "TODO", false},
		{"empty bare string", `""`, false, "", false},
		{"array untouched", `[1]`, false, "", false},
		{"number untouched", `5`, false, "", false},
		{"has pattern", `{"pattern":"x","path":"a"}`, false, "x", true},
		{"alias query", `{"path":"main.go","query":"Handle"}`, true, "Handle", true},
		{"alias text", `{"text":"zz"}`, true, "zz", false},
		{"path regex meta", `{"path":"foo.*bar"}`, true, "foo.*bar", false},
		{"path without separator", `{"path":"needle"}`, true, "needle", false},
		{"windows path is a real path", `{"path":"controllers\\auth.go"}`, false, "", true},
		{"unix path is a real path", `{"path":"src/dir"}`, false, "", true},
		{"nothing at all", `{}`, false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, changed := RepairGrepToolCall(c.in)
			if changed != c.changed {
				t.Fatalf("changed=%v want %v (out %s)", changed, c.changed, out)
			}
			if !changed {
				if out != c.in {
					t.Fatalf("unchanged input must be returned verbatim, got %q", out)
				}
				return
			}
			if got := gjson.Get(out, "pattern").String(); got != c.pattern {
				t.Fatalf("pattern=%q want %q", got, c.pattern)
			}
			if gjson.Get(out, "path").Exists() != c.hasPath {
				t.Fatalf("path presence wrong in %s", out)
			}
			if gjson.Get(out, "query").Exists() {
				t.Fatalf("alias key should be removed: %s", out)
			}
		})
	}
}

func TestRepairGlobToolCall_Table(t *testing.T) {
	cases := []struct {
		name, in string
		changed  bool
		pattern  string
		hasPath  bool
	}{
		{"not json value", ``, false, "", false},
		{"bare string", `"**/*.go"`, true, "**/*.go", false},
		{"empty bare string", `""`, false, "", false},
		{"array untouched", `["a"]`, false, "", false},
		{"has pattern", `{"pattern":"*.go"}`, false, "*.go", false},
		{"alias glob", `{"glob":"*.go","path":"src"}`, true, "*.go", true},
		{"alias search", `{"search":"*.ts"}`, true, "*.ts", false},
		{"path is glob", `{"path":"src/**/*.go"}`, true, "src/**/*.go", false},
		{"plain path stays", `{"path":"src/dir"}`, false, "", true},
		{"empty object", `{}`, false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, changed := RepairGlobToolCall(c.in)
			if changed != c.changed {
				t.Fatalf("changed=%v want %v (out %s)", changed, c.changed, out)
			}
			if !changed {
				if out != c.in {
					t.Fatalf("unchanged input must be returned verbatim, got %q", out)
				}
				return
			}
			if got := gjson.Get(out, "pattern").String(); got != c.pattern {
				t.Fatalf("pattern=%q want %q", got, c.pattern)
			}
			if gjson.Get(out, "path").Exists() != c.hasPath {
				t.Fatalf("path presence wrong in %s", out)
			}
		})
	}
}
