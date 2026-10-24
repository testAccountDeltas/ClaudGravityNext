package atomicfile

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestWriteFile_UnwritableDirReturnsError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no", "such", "dir", "f.json")
	if err := WriteFile(missing, []byte("x")); err == nil {
		t.Fatal("writing into a missing directory must fail")
	}
	// A path whose parent is a regular file cannot be a directory either.
	file := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(file, "child.json"), []byte("x")); err == nil {
		t.Fatal("parent that is a file must fail")
	}
}

func TestWriteFile_TargetIsDirectoryFailsAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(target, []byte("x")); err == nil {
		t.Fatal("renaming a file over a directory must fail")
	}
	assertNoTemps(t, dir)
}

func TestWriteFile_StaleTempFilesAreIgnoredAndUntouched(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	stale := filepath.Join(dir, "a.json.12345.tmp")
	if err := os.WriteFile(stale, []byte("crashed-writer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("good")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "good" {
		t.Fatalf("got %q", b)
	}
	if b, err := os.ReadFile(stale); err != nil || string(b) != "crashed-writer" {
		t.Fatal("another writer's temp file must not be consumed or deleted")
	}
}

func TestWriteFile_EmptyAndLargePayloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "p.bin")
	for _, data := range [][]byte{{}, bytes.Repeat([]byte("z"), 3<<20)} {
		if err := WriteFile(path, data); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(path); !bytes.Equal(b, data) {
			t.Fatalf("payload of %d bytes corrupted (read %d)", len(data), len(b))
		}
	}
}

func TestWriteFile_TwentyConcurrentWritersLeaveACompleteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.json")
	payload := func(i int) string { return fmt.Sprintf(`{"writer":%d,"pad":"%s"}`, i, strings.Repeat("p", 4096)) }
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if err := WriteFile(path, []byte(payload(i))); err != nil {
					// Windows may transiently refuse a rename over an open reader.
					t.Logf("writer %d: %v", i, err)
				}
			}
		}(i)
	}
	// Readers run concurrently: every observed content must be one whole payload.
	stop := make(chan struct{})
	var rd sync.WaitGroup
	rd.Add(1)
	go func() {
		defer rd.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if b, err := os.ReadFile(path); err == nil && !validPayload(string(b)) {
				t.Errorf("torn read: %d bytes", len(b))
				return
			}
		}
	}()
	wg.Wait()
	close(stop)
	rd.Wait()

	b, err := os.ReadFile(path)
	if err != nil || !validPayload(string(b)) {
		t.Fatalf("final file incomplete: %d bytes err=%v", len(b), err)
	}
	assertNoTemps(t, dir)
}

func validPayload(s string) bool {
	for i := 0; i < 20; i++ {
		if s == fmt.Sprintf(`{"writer":%d,"pad":"%s"}`, i, strings.Repeat("p", 4096)) {
			return true
		}
	}
	return false
}

func assertNoTemps(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") && !strings.Contains(e.Name(), "12345") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}
