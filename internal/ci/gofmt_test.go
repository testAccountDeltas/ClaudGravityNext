package ci

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGofmtClean fails on any Go source (tests included) that gofmt would
// rewrite. It uses go/format directly, so it needs no external tool and runs
// in a plain `go test ./...` locally and in CI alike. Line endings are
// normalised first: a CRLF checkout (core.autocrlf) is not a formatting
// defect.
func TestGofmtClean(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("failed to resolve root directory: %v", err)
	}
	var unformatted, broken []string
	for _, dir := range []string{"internal", "pkg", "cmd"} {
		_ = filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				if info.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			src = bytes.ReplaceAll(src, []byte("\r\n"), []byte("\n"))
			rel, _ := filepath.Rel(root, path)
			out, err := format.Source(src)
			if err != nil {
				broken = append(broken, rel+": "+err.Error())
				return nil
			}
			if !bytes.Equal(src, out) {
				unformatted = append(unformatted, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	if len(broken) > 0 {
		t.Errorf("files that do not parse:\n  %s", strings.Join(broken, "\n  "))
	}
	if len(unformatted) > 0 {
		t.Errorf("run `gofmt -w` on these %d file(s):\n  %s", len(unformatted), strings.Join(unformatted, "\n  "))
	}
}

// TestGofmtCheckDetectsAnUnformattedSource keeps the check honest: the same
// comparison must flag a badly formatted snippet and accept a clean one.
func TestGofmtCheckDetectsAnUnformattedSource(t *testing.T) {
	bad := []byte("package p\nfunc  f( ) {\nreturn\n}\n")
	out, err := format.Source(bad)
	if err != nil || bytes.Equal(bad, out) {
		t.Fatalf("a badly formatted snippet must differ from its formatted form (err=%v)", err)
	}
	if again, _ := format.Source(out); !bytes.Equal(out, again) {
		t.Fatal("formatting must be idempotent")
	}
}
