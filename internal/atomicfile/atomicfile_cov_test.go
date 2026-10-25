package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCov_MissingDirectoryErrors(t *testing.T) {
	err := WriteFile(filepath.Join(t.TempDir(), "no", "such", "dir", "f.json"), []byte("x"))
	if err == nil {
		t.Fatal("expected error when the directory does not exist")
	}
}

func TestCov_RenameFailureLeavesNoTempAndKeepsTarget(t *testing.T) {
	dir := t.TempDir()
	// Target is a non-empty directory: rename of a file over it must fail.
	target := filepath.Join(dir, "target")
	if err := os.MkdirAll(filepath.Join(target, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(target, []byte("data")); err == nil {
		t.Fatal("expected rename error")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "target" {
		t.Fatalf("temp file leaked: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(target, "child")); err != nil {
		t.Fatal("original target must be untouched")
	}
}

func TestCov_OverwriteReplacesContentsAndEmptyData(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.json")
	if err := WriteFile(p, []byte("first-longer-content")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(p, []byte("2")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "2" {
		t.Fatalf("got %q", b)
	}
	if err := WriteFile(p, nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); len(b) != 0 {
		t.Fatal("expected empty file")
	}
}
