// Package ci implements automated CI assertions for ClaudGravityNext.
// Enforces the architectural invariants defined in CLAUDE.md Part 4.1.
package ci

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// MaxFileLineCeiling is the hard ceiling of 400 lines per production file
// specified in CLAUDE.md Part 4.1 (target 80-250 lines, hard ceiling 400).
const MaxFileLineCeiling = 400

func TestFileSizeCeiling(t *testing.T) {
	// Root of repository is two levels up from internal/ci
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("failed to resolve root directory: %v", err)
	}

	dirsToScan := []string{
		filepath.Join(root, "internal"),
		filepath.Join(root, "pkg"),
		filepath.Join(root, "cmd"),
	}

	var violations []string

	for _, dir := range dirsToScan {
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			// Only inspect Go production files; test files (*_test.go) are exempt
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			lines, err := countLines(path)
			if err != nil {
				return err
			}

			if lines > MaxFileLineCeiling {
				relPath, _ := filepath.Rel(root, path)
				violations = append(violations, relPath)
				t.Errorf("file-size ceiling violation: %s has %d lines (hard ceiling is %d lines)", relPath, lines, MaxFileLineCeiling)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("failed to walk %s: %v", dir, err)
		}
	}

	if len(violations) > 0 {
		t.Fatalf("found %d production file(s) exceeding %d lines: %v", len(violations), MaxFileLineCeiling, violations)
	}
}

func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lines := 0
	for scanner.Scan() {
		lines++
	}
	return lines, scanner.Err()
}
