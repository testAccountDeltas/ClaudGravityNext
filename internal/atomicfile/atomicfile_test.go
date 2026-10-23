package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFile_NeverLeavesOnlyATempFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.json")
	if err := WriteFile(path, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("must rename the temp file into place, not leave it dangling")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != `{"a":1}` {
		t.Fatalf("got data=%q err=%v", data, err)
	}
}

func TestWriteFile_OverwritesExistingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.json")
	if err := WriteFile(path, []byte("first")); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteFile(path, []byte("second")); err != nil {
		t.Fatalf("second write: %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "second" {
		t.Fatalf("expected overwrite, got %q", data)
	}
}
