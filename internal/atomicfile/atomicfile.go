// Package atomicfile holds WriteFile, a crash-safe file write originally
// embedded in v1's config.go but genuinely needed by TWO unrelated
// packages: internal/config (its own config.json) and internal/accounts
// (OAuth credential JSON files) — neither has a real reason to depend on
// the other just to reach a generic filesystem helper, so it gets its
// own leaf package, same single-ownership rule as internal/sessionkey,
// internal/fakeids, and internal/toolkind elsewhere in this migration.
package atomicfile

import (
	"os"
	"path/filepath"
)

// WriteFile writes via a temp file + os.Rename instead of a direct
// os.WriteFile, so a process killed mid-write (crash, OOM-kill, power
// loss) can never leave the target half-written — os.WriteFile truncates
// the real file in place before writing the new bytes, so a process that
// dies between the truncate and the final byte leaves it empty/corrupt.
func WriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	pattern := filepath.Base(path) + ".*.tmp"
	tmpFile, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	closed := false
	defer func() {
		if !closed {
			_ = tmpFile.Close()
		}
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	closed = true
	return os.Rename(tmpPath, path)
}
