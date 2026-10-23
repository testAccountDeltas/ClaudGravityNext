// Phase 1 characterization test for the ported logx package — proves
// this port behaves identically to v1's logger.go for the two things
// that matter: the exact log line format, and the rotation policy
// (512KB threshold, 3 backups, oldest dropped).
package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var logLinePattern = regexp.MustCompile(`^\[\d{2}:\d{2}:\d{2}\] hello 5\n$`)

func TestLogf_LineFormatMatchesV1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	l := &RotatingLogger{path: path}

	l.Logf("hello %d", 5)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !logLinePattern.Match(data) {
		t.Fatalf("log line format changed from v1: got %q", string(data))
	}
}

func TestRotate_RotatesAtThresholdAndDropsOldest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	l := &RotatingLogger{path: path}

	// Pre-seed three generations of backups so this test also proves the
	// shift-and-drop chain (.1->.2->.2->.3->dropped), not just a single
	// rotation from nothing.
	for i, content := range []string{"gen1", "gen2", "gen3"} {
		bpath := fmt.Sprintf("%s.%d", path, i+1)
		if err := os.WriteFile(bpath, []byte(content), 0644); err != nil {
			t.Fatalf("seed backup %s: %v", bpath, err)
		}
	}

	// Oversized current log file triggers rotation on the NEXT Logf call.
	oversized := make([]byte, LogMaxBytes)
	if err := os.WriteFile(path, oversized, 0644); err != nil {
		t.Fatalf("seed oversized log: %v", err)
	}

	l.Logf("trigger rotation")

	// .1 must now contain what was the oversized current file content
	// (all zero bytes, since that's what we seeded) — not "gen1".
	gen1, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read .1 after rotation: %v", err)
	}
	if len(gen1) != LogMaxBytes {
		t.Fatalf(".1 after rotation should be the rotated-out oversized file (%d bytes), got %d bytes", LogMaxBytes, len(gen1))
	}

	// .2 must now contain what was "gen1" (shifted up one slot).
	gen2, err := os.ReadFile(path + ".2")
	if err != nil {
		t.Fatalf("read .2 after rotation: %v", err)
	}
	if string(gen2) != "gen1" {
		t.Fatalf(".2 after rotation should be the old .1 content %q, got %q", "gen1", string(gen2))
	}

	// .3 must now contain what was "gen2" — "gen3" (the oldest, LogBackups-th
	// generation) must have been deleted, not shifted to a .4 that doesn't exist.
	gen3, err := os.ReadFile(path + ".3")
	if err != nil {
		t.Fatalf("read .3 after rotation: %v", err)
	}
	if string(gen3) != "gen2" {
		t.Fatalf(".3 after rotation should be the old .2 content %q, got %q", "gen2", string(gen3))
	}
	if _, err := os.Stat(fmt.Sprintf("%s.%d", path, LogBackups+1)); !os.IsNotExist(err) {
		t.Fatalf("a .%d backup must not exist — oldest generation should be dropped, not kept", LogBackups+1)
	}

	// The new current file must contain only the freshly logged line, not
	// a leftover of the rotated-out content.
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read current log after rotation: %v", err)
	}
	if len(current) == LogMaxBytes {
		t.Fatalf("current log file after rotation still has the old oversized content — rotation didn't truncate/replace it")
	}
}

func TestRotate_NoOpBelowThreshold(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.log")
	l := &RotatingLogger{path: path}

	l.Logf("first line")
	l.Logf("second line")

	if _, err := os.Stat(path + ".1"); !os.IsNotExist(err) {
		t.Fatalf("no .1 backup should exist while the log file stays under %d bytes", LogMaxBytes)
	}
}

func TestInitLogger_EmptyPathKeepsDefault(t *testing.T) {
	before := globalLogger.path
	InitLogger("")
	if globalLogger.path != before {
		t.Fatalf("InitLogger(\"\") must leave the configured path unchanged, got %q want %q", globalLogger.path, before)
	}
}
