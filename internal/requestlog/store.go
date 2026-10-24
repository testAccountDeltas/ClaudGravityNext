// Package requestlog is a port of superrelay (v1)'s request_logger.go,
// split across four files by responsibility per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 4.3: store.go
// (RequestLog/SQLiteLogStore/InitLogStore/Add/Clear/Close — this file),
// query.go (Query/QuerySessions/GetSessionDetail), agentinfo.go
// (ParseAgentInfo/DeriveSessionID), mask.go (MaskKey). Every function
// body is unchanged from v1 except one real, documented fix below.
//
// v1's Add() called globalWSHub.BroadcastLog/BroadcastSessionsUpdate
// directly — a hidden dependency on ws_hub.go that only worked because
// v1 is one `package main`. internal/requestlog must NOT import
// internal/dashboard: the dashboard displays logs, logs don't know about
// the dashboard — the same dependency-direction fix already applied to
// internal/config's relationship with internal/accounts in this same
// phase. Add() now calls an optional Broadcaster interface, wired by
// the composition root (cmd/gateway/main.go) once internal/dashboard
// exists, instead of a hardcoded cross-package global.
package requestlog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type RequestLog struct {
	ID              uint64    `json:"id"`
	Timestamp       time.Time `json:"timestamp"`
	Key             string    `json:"-"`
	KeyMasked       string    `json:"key_masked"`
	Account         string    `json:"account"`
	SessionID       string    `json:"session_id"`
	AgentKey        string    `json:"agent_key"`  // "main", "sub:fork", "sub:explore", "auxiliary"
	AgentType       string    `json:"agent_type"` // "main", "subagent", "auxiliary"
	SubagentName    string    `json:"subagent_name,omitempty"`
	InstanceID      string    `json:"instance_id,omitempty"` // x-claude-code-prompt-id: stable per Task-tool invocation, distinguishes parallel same-named subagents
	Model           string    `json:"model"`
	TTFT            int64     `json:"ttft_ms"`
	TotalDur        int64     `json:"total_dur_ms"`
	InTokens        int64     `json:"in_tokens"`
	OutTokens       int64     `json:"out_tokens"`
	CacheReadTokens int64     `json:"cache_read_tokens"`
	CacheHitRate    float64   `json:"cache_hit_rate"`
	Tools           []string  `json:"tools"`
	WasPanicked     bool      `json:"was_panicked"`
	Shrunk          bool      `json:"shrunk"`
	Error           string    `json:"error,omitempty"`
	Status          int       `json:"status"`
}

type SQLiteLogStore struct {
	mu     sync.RWMutex
	db     *sql.DB
	stopCh chan struct{}
}

var globalLogStore = &SQLiteLogStore{}

// Add logs one request through the package-level singleton store — the
// same package-level-singleton-plus-package-function convention already
// used by InitLogStore/InitSpecCapture elsewhere in this migration, so a
// caller outside this package (internal/websearch, and eventually
// internal/orchestrate) doesn't need the SQLiteLogStore type threaded
// through as a parameter just to log one request, the same way v1's
// websearch.go called the package-private globalLogStore.Add directly
// (only possible there because v1 is one package main).
func Add(entry RequestLog) { globalLogStore.Add(entry) }

// Clear, QuerySessions, GetSessionDetail, and Query are the same
// package-level-singleton convenience wrappers as Add, for the same
// reason: internal/dashboard (ws_hub.go's port) and the future
// internal/httpapi need to reach this store without importing its
// internal SQLiteLogStore type.
func Clear()                                   { globalLogStore.Clear() }
func Close()                                   { globalLogStore.Close() }
func QuerySessions(limit int) []SessionSummary { return globalLogStore.QuerySessions(limit) }
func GetSessionDetail(sessionID string) (*SessionSummary, []RequestLog) {
	return globalLogStore.GetSessionDetail(sessionID)
}
func Query(keyFilter, accountFilter, sessionFilter, agentFilter string, onlyErrors bool, limit int) ([]RequestLog, LogStats) {
	return globalLogStore.Query(keyFilter, accountFilter, sessionFilter, agentFilter, onlyErrors, limit)
}

// Broadcaster is implemented by internal/dashboard's WSHub. See this
// file's doc comment for why requestlog calls it through an interface
// rather than importing dashboard directly.
type Broadcaster interface {
	BroadcastLog(entry *RequestLog)
	BroadcastSessionsUpdate()
}

var broadcaster Broadcaster

// SetBroadcaster wires a dashboard (or any other) subscriber in. Safe to
// leave unset — Add() simply skips broadcasting, as nothing in this
// package or its tests requires a dashboard to exist.
func SetBroadcaster(b Broadcaster) { broadcaster = b }

func InitLogStore(dbPath string) error {
	if dbPath == "" {
		dbPath = "superrelay_logs.db"
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return fmt.Errorf("open sqlite db: %w", err)
	}

	// Schema creation
	schema := `
	CREATE TABLE IF NOT EXISTS request_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp DATETIME NOT NULL,
		client_key TEXT NOT NULL,
		key_masked TEXT NOT NULL,
		account TEXT NOT NULL,
		session_id TEXT NOT NULL,
		agent_key TEXT NOT NULL DEFAULT 'main',
		agent_type TEXT NOT NULL DEFAULT 'main',
		subagent_name TEXT NOT NULL DEFAULT '',
		instance_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL,
		ttft_ms INTEGER NOT NULL DEFAULT 0,
		total_dur_ms INTEGER NOT NULL DEFAULT 0,
		in_tokens INTEGER NOT NULL DEFAULT 0,
		out_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_tokens INTEGER NOT NULL DEFAULT 0,
		cache_hit_rate REAL NOT NULL DEFAULT 0,
		tools_json TEXT NOT NULL DEFAULT '[]',
		was_panicked INTEGER NOT NULL DEFAULT 0,
		shrunk INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		status INTEGER NOT NULL DEFAULT 200
	);
	CREATE INDEX IF NOT EXISTS idx_logs_timestamp ON request_logs(timestamp DESC);
	CREATE INDEX IF NOT EXISTS idx_logs_session ON request_logs(session_id);
	CREATE INDEX IF NOT EXISTS idx_logs_account ON request_logs(account);
	CREATE INDEX IF NOT EXISTS idx_logs_client_key ON request_logs(client_key);
	CREATE INDEX IF NOT EXISTS idx_logs_agent ON request_logs(agent_key);
	`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("exec schema: %w", err)
	}
	// Migration for DBs created before instance_id existed; sqlite has no
	// "ADD COLUMN IF NOT EXISTS", so just swallow the "duplicate column" error.
	_, _ = db.Exec(`ALTER TABLE request_logs ADD COLUMN instance_id TEXT NOT NULL DEFAULT ''`)

	globalLogStore.mu.Lock()
	if globalLogStore.stopCh != nil {
		close(globalLogStore.stopCh)
	}
	stopCh := make(chan struct{})
	globalLogStore.stopCh = stopCh
	oldDB := globalLogStore.db
	globalLogStore.db = db
	globalLogStore.mu.Unlock()
	// A second InitLogStore used to orphan the previous *sql.DB (open file
	// handles + WAL until process exit). Add holds mu.RLock across its Exec,
	// so by the time the swap above completed no writer is mid-statement.
	if oldDB != nil {
		_ = oldDB.Close()
	}

	// Clean up records older than 24h at startup and periodically
	go func() {
		cleanupOldLogs(db)
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				cleanupOldLogs(db)
			}
		}
	}()

	// WAL mode never shrinks its -wal file on its own without an explicit
	// checkpoint; left unattended it grows unbounded (seen in practice: 4MB+
	// WAL against a 220KB main db file) since nothing else in this gateway
	// ever asked SQLite to fold it back in.
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				_, _ = db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
			}
		}
	}()

	return nil
}

func cleanupOldLogs(db *sql.DB) {
	cutoff := time.Now().Add(-24 * time.Hour)
	_, _ = db.Exec("DELETE FROM request_logs WHERE timestamp < ?", cutoff)
}

// Close checkpoints the WAL back into the main db file and closes the
// connection. Called on graceful shutdown so a killed process doesn't leave
// an uncheckpointed -wal file (or, worse, miss flushing the last batch of
// in-flight writes) behind.
func (s *SQLiteLogStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopCh != nil {
		close(s.stopCh)
		s.stopCh = nil
	}
	if s.db == nil {
		return
	}
	_, _ = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	_ = s.db.Close()
	s.db = nil
}

func (s *SQLiteLogStore) Add(entry RequestLog) {
	s.mu.RLock()
	db := s.db
	if db == nil {
		s.mu.RUnlock()
		return
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	if entry.Tools == nil {
		entry.Tools = []string{}
	}
	if entry.AgentKey == "" {
		entry.AgentKey = "main"
	}
	if entry.AgentType == "" {
		entry.AgentType = "main"
	}
	toolsBytes, _ := json.Marshal(entry.Tools)

	wasPanickedInt := 0
	if entry.WasPanicked {
		wasPanickedInt = 1
	}
	shrunkInt := 0
	if entry.Shrunk {
		shrunkInt = 1
	}

	query := `
	INSERT INTO request_logs (
		timestamp, client_key, key_masked, account, session_id,
		agent_key, agent_type, subagent_name, instance_id, model,
		ttft_ms, total_dur_ms, in_tokens, out_tokens, cache_read_tokens,
		cache_hit_rate, tools_json, was_panicked, shrunk, error, status
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);
	`
	// Only the masked form of the key is ever persisted to disk, in both
	// columns — storing the raw tenant API key in a plaintext SQLite file
	// (and returning it via /api/logs) was an avoidable sensitive-data-at-
	// rest exposure the dashboard password doesn't fully excuse.
	_, _ = db.Exec(
		query,
		entry.Timestamp, entry.KeyMasked, entry.KeyMasked, entry.Account, entry.SessionID,
		entry.AgentKey, entry.AgentType, entry.SubagentName, entry.InstanceID, entry.Model,
		entry.TTFT, entry.TotalDur, entry.InTokens, entry.OutTokens, entry.CacheReadTokens,
		entry.CacheHitRate, string(toolsBytes), wasPanickedInt, shrunkInt, entry.Error, entry.Status,
	)
	s.mu.RUnlock()

	if broadcaster != nil {
		go func(e RequestLog) {
			broadcaster.BroadcastLog(&e)
			broadcaster.BroadcastSessionsUpdate()
		}(entry)
	}
}

func (s *SQLiteLogStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return
	}
	_, _ = s.db.Exec("DELETE FROM request_logs")
}
