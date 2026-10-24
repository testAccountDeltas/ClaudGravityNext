// Part of the accounts package — see pool.go for the package doc comment.
// Holds the single serialized read-modify-write path for account auth JSON
// files. ToggleAccountDisabled, RefreshAccessToken (rotated refresh token)
// and the project-id probe each used to ReadFile -> sjson -> WriteFile on
// their own: two interleaved writers each read the same stale bytes and the
// later write silently erased the earlier one's field (a rotated
// refresh_token overwritten by stale data = a dead account after restart).
package accounts

import (
	"os"
	"sync"

	"claudgravity/internal/atomicfile"
)

// authFileMu serializes every read-modify-write of an account auth file.
// One mutex for all files: these writes are rare and tiny, so per-file
// granularity would buy nothing but a lock map to leak.
var authFileMu sync.Mutex

// updateAuthFile re-reads filePath INSIDE the lock, applies mutate to the
// fresh bytes and writes the result atomically. A missing/unreadable file or
// a mutate error leaves the file untouched. No-op for an empty path.
func updateAuthFile(filePath string, mutate func(data []byte) ([]byte, error)) {
	if filePath == "" {
		return
	}
	authFileMu.Lock()
	defer authFileMu.Unlock()
	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}
	updated, err := mutate(data)
	if err != nil {
		return
	}
	_ = atomicfile.WriteFile(filePath, updated)
}
