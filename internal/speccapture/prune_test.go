package speccapture

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func existing(t *testing.T, dir string) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		out[e.Name()] = true
	}
	return out
}

func touch(t *testing.T, path string, size int, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestPruneOld_ByFilenameDate(t *testing.T) {
	dir := t.TempDir()
	day := func(back int) string { return time.Now().UTC().AddDate(0, 0, -back).Format("2006-01-02") + ".ndjson" }
	for _, n := range []string{day(0), day(3), day(10), day(40), "notes.ndjson", "2020-13-45.ndjson", "2020-01-01.txt", "2020-01-01.ndjson.bak"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "2000-01-01.ndjson.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	// An old mtime must not matter: the date comes from the filename.
	old := time.Now().AddDate(-1, 0, 0)
	_ = os.Chtimes(filepath.Join(dir, day(0)), old, old)

	s := &SpecCaptureStore{dir: dir, retain: 7}
	s.pruneOld()
	got := existing(t, dir)
	for _, keep := range []string{day(0), day(3), "notes.ndjson", "2020-13-45.ndjson", "2020-01-01.txt", "2020-01-01.ndjson.bak", "2000-01-01.ndjson.d"} {
		if !got[keep] {
			t.Errorf("%s must be kept", keep)
		}
	}
	for _, gone := range []string{day(10), day(40)} {
		if got[gone] {
			t.Errorf("%s must be pruned", gone)
		}
	}
}

func TestPruneOld_RetainNonPositiveKeepsEverything(t *testing.T) {
	for _, retain := range []int{0, -3} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "2001-01-01.ndjson"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		(&SpecCaptureStore{dir: dir, retain: retain}).pruneOld()
		if !existing(t, dir)["2001-01-01.ndjson"] {
			t.Fatalf("retain=%d must disable pruning", retain)
		}
	}
	(&SpecCaptureStore{dir: filepath.Join(t.TempDir(), "missing"), retain: 5}).pruneOld() // unreadable dir: no panic
}

func rawStore(dir string, maxSessions int, maxBytes int64) *SpecCaptureStore {
	return &SpecCaptureStore{rawEnabled: true, rawDir: dir, rawMaxSessions: maxSessions, rawMaxBytes: maxBytes}
}

func TestPruneRawChunks_MaxBytesBranchDropsOldestFirst(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for i := 1; i <= 4; i++ { // 100 bytes each, rid 1 oldest
		touch(t, filepath.Join(dir, strconv.Itoa(i)+".ndjson"), 100, now.Add(time.Duration(i)*time.Minute))
	}
	rawStore(dir, 100, 250).pruneRawChunks() // sessions fine, 400B > 250B
	got := existing(t, dir)
	if len(got) != 2 || !got["3.ndjson"] || !got["4.ndjson"] {
		t.Fatalf("want the 2 newest (200B <= 250B) kept, got %v", got)
	}
}

func TestPruneRawChunks_UnderBothLimitsAndDisabledAreNoOps(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	touch(t, filepath.Join(dir, "1.ndjson"), 10, now)
	touch(t, filepath.Join(dir, "2.ndjson"), 10, now)
	rawStore(dir, 5, 1000).pruneRawChunks()
	if len(existing(t, dir)) != 2 {
		t.Fatal("under both limits: nothing may be deleted")
	}
	s := rawStore(dir, 0, 1)
	s.rawEnabled = false
	s.pruneRawChunks()
	if len(existing(t, dir)) != 2 {
		t.Fatal("disabled raw capture must not prune")
	}
}

func TestPruneRawChunks_BothLimitsAndSubdirIgnored(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		touch(t, filepath.Join(dir, strconv.Itoa(i)+".ndjson"), 50, now.Add(time.Duration(i)*time.Minute))
	}
	rawStore(dir, 3, 120).pruneRawChunks() // count wants 3 (150B), bytes wants <=120B => 2
	got := existing(t, dir)
	if !got["sub"] || !got["5.ndjson"] || !got["4.ndjson"] || len(got) != 3 {
		t.Fatalf("got %v", got)
	}
}
