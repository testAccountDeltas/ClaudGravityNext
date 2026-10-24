package requestlog

import (
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func countRows(t *testing.T, s *SQLiteLogStore) int {
	t.Helper()
	s.mu.RLock()
	defer s.mu.RUnlock()
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM request_logs").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestInitLogStore_MigratesOldSchemaWithoutInstanceID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE request_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, timestamp DATETIME NOT NULL, client_key TEXT NOT NULL,
		key_masked TEXT NOT NULL, account TEXT NOT NULL, session_id TEXT NOT NULL,
		agent_key TEXT NOT NULL DEFAULT 'main', agent_type TEXT NOT NULL DEFAULT 'main',
		subagent_name TEXT NOT NULL DEFAULT '', model TEXT NOT NULL,
		ttft_ms INTEGER NOT NULL DEFAULT 0, total_dur_ms INTEGER NOT NULL DEFAULT 0,
		in_tokens INTEGER NOT NULL DEFAULT 0, out_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0, cache_hit_rate REAL NOT NULL DEFAULT 0,
		tools_json TEXT NOT NULL DEFAULT '[]', was_panicked INTEGER NOT NULL DEFAULT 0,
		shrunk INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', status INTEGER NOT NULL DEFAULT 200)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec(`INSERT INTO request_logs (timestamp, client_key, key_masked, account, session_id, model)
		VALUES (?, 'k', 'k', 'legacy@example.com', 's-old', 'm')`, time.Now()); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()

	t.Cleanup(Close)
	if err := InitLogStore(path); err != nil {
		t.Fatalf("InitLogStore on old schema: %v", err)
	}
	Add(RequestLog{Account: "new@example.com", SessionID: "s-new", Model: "m", InstanceID: "inst-1", KeyMasked: "k"})
	logs, _ := Query("", "", "", "", false, 10)
	got := map[string]string{}
	for _, l := range logs {
		got[l.Account] = l.InstanceID
	}
	if len(got) != 2 || got["legacy@example.com"] != "" || got["new@example.com"] != "inst-1" {
		t.Fatalf("migration lost data or the new column: %v", got)
	}
}

func TestCleanupOldLogs_PurgesOlderThan24h(t *testing.T) {
	s := newTestStore(t)
	now := time.Now()
	s.Add(RequestLog{Account: "old", SessionID: "a", Model: "m", Timestamp: now.Add(-25 * time.Hour)})
	s.Add(RequestLog{Account: "edge-old", SessionID: "b", Model: "m", Timestamp: now.Add(-24*time.Hour - time.Minute)})
	s.Add(RequestLog{Account: "recent", SessionID: "c", Model: "m", Timestamp: now.Add(-23 * time.Hour)})
	s.Add(RequestLog{Account: "fresh", SessionID: "d", Model: "m"})
	cleanupOldLogs(s.db)
	logs, _ := s.Query("", "", "", "", false, 10)
	accts := map[string]bool{}
	for _, l := range logs {
		accts[l.Account] = true
	}
	if len(accts) != 2 || !accts["recent"] || !accts["fresh"] {
		t.Fatalf("want only recent+fresh, got %v", accts)
	}
}

func TestClear_RemovesAllRowsAndIsSafeWhenClosed(t *testing.T) {
	s := newTestStore(t)
	s.Add(RequestLog{Account: "a", SessionID: "s", Model: "m"})
	if countRows(t, s) != 1 {
		t.Fatal("row not added")
	}
	s.Clear()
	if countRows(t, s) != 0 {
		t.Fatal("Clear left rows")
	}
	s.Close()
	s.Clear() // closed store: no panic
	s.Add(RequestLog{Account: "a"})
	s.Close() // double close: no panic
}

func TestInitLogStore_SecondCallClosesPreviousDB(t *testing.T) {
	s := newTestStore(t)
	first := s.db
	if err := InitLogStore(filepath.Join(t.TempDir(), "second.db")); err != nil {
		t.Fatal(err)
	}
	if first == s.db {
		t.Fatal("db handle not replaced")
	}
	if err := first.Ping(); err == nil {
		t.Fatal("previous *sql.DB leaked: still open after re-init")
	}
	s.Add(RequestLog{Account: "x", SessionID: "s", Model: "m"})
	if countRows(t, s) != 1 {
		t.Fatal("new store must be writable")
	}
}

func TestStore_ConcurrentAddClearClose(t *testing.T) {
	s := newTestStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch {
			case i%10 == 7:
				s.Clear()
			case i == 49:
				s.Close()
			default:
				s.Add(RequestLog{Account: "a", SessionID: "s", Model: "m", Tools: []string{"t"}})
			}
		}(i)
	}
	wg.Wait()
	s.Add(RequestLog{Account: "after-close"}) // must be a silent no-op
	s.Clear()
}
