// Part of the toolsig package — see toolsig.go for the package doc
// comment (point 2: restart survival). This file is the SQLite-backed
// fallback checked on a RAM miss and written on every Set, opened
// against the SAME db file internal/requestlog already uses
// (superrelay_logs.db by default) so there's exactly one SQLite file on
// disk to back up/inspect, not two. A second *sql.DB handle to the same
// WAL-mode file is fine — SQLite's own locking handles it, and this
// package must not import internal/requestlog just to share its *sql.DB
// (that would invert the leaf-package direction toolsig.go's doc comment
// already explains toolsig needs for internal/stream and
// internal/translate).
package toolsig

import (
	"database/sql"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// persistedTTL is far longer than the RAM store's staleAfter (2h): RAM
// covers an active conversation; this covers "the gateway restarted
// between this turn and the next one the user sends tomorrow."
const persistedTTL = 7 * 24 * time.Hour

var (
	persistMu sync.RWMutex
	persistDB *sql.DB
	stopCh    chan struct{}
)

// InitPersistence opens (or reopens) the SQLite-backed fallback store.
// dbPath "" uses the same default internal/requestlog.InitLogStore uses.
// Safe to call before internal/requestlog.InitLogStore, after it, or not
// at all — every other function in this file is a no-op fail-open when
// no db has been opened, exactly like the RAM-only cache behaved before
// this file existed.
func InitPersistence(dbPath string) error {
	if dbPath == "" {
		dbPath = "superrelay_logs.db"
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return err
	}
	schema := `
	CREATE TABLE IF NOT EXISTS thought_signatures (
		key TEXT PRIMARY KEY,
		signature TEXT NOT NULL,
		family TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_thoughtsig_expires ON thought_signatures(expires_at);
	`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return err
	}

	persistMu.Lock()
	if stopCh != nil {
		close(stopCh)
	}
	stop := make(chan struct{})
	stopCh = stop
	oldDB := persistDB
	persistDB = db
	persistMu.Unlock()
	if oldDB != nil {
		_ = oldDB.Close()
	}

	go func() {
		pruneExpired(db)
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				pruneExpired(db)
			}
		}
	}()

	return nil
}

// ClosePersistence closes the fallback store. Called on graceful shutdown
// next to internal/requestlog.Close().
func ClosePersistence() {
	persistMu.Lock()
	defer persistMu.Unlock()
	if stopCh != nil {
		close(stopCh)
		stopCh = nil
	}
	if persistDB == nil {
		return
	}
	_ = persistDB.Close()
	persistDB = nil
}

func pruneExpired(db *sql.DB) {
	_, _ = db.Exec("DELETE FROM thought_signatures WHERE expires_at <= ?", time.Now().Unix())
}

func getDB() *sql.DB {
	persistMu.RLock()
	defer persistMu.RUnlock()
	return persistDB
}

// persistSet upserts key's signature/family async — the hot path (an
// in-flight SSE stream recording a signature as it's emitted) must never
// block on a disk write.
func persistSet(key, signature, family string) {
	db := getDB()
	if db == nil || key == "" || signature == "" {
		return
	}
	now := time.Now()
	go func() {
		_, _ = db.Exec(
			`INSERT INTO thought_signatures (key, signature, family, created_at, expires_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(key) DO UPDATE SET
				signature = excluded.signature,
				family = excluded.family,
				expires_at = excluded.expires_at`,
			key, signature, family, now.Unix(), now.Add(persistedTTL).Unix(),
		)
	}()
}

// persistGet looks key up synchronously — callers are already on the hot
// path of "RAM cache missed," which in practice means this only runs
// right after a restart or once a 2h-stale entry has aged out, not on
// every turn.
func persistGet(key string) (signature, family string, ok bool) {
	db := getDB()
	if db == nil || key == "" {
		return "", "", false
	}
	row := db.QueryRow(
		`SELECT signature, family FROM thought_signatures WHERE key = ? AND expires_at > ?`,
		key, time.Now().Unix(),
	)
	var sig, fam string
	if err := row.Scan(&sig, &fam); err != nil {
		return "", "", false
	}
	return sig, fam, true
}
