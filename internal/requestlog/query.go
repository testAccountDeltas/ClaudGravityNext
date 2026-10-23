// Part of the requestlog package's port from superrelay (v1)'s
// request_logger.go — see store.go for the split rationale. This file
// holds the flat, filtered request list (Query) and LogStats;
// sessions.go holds the session-oriented reads.
package requestlog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

type LogStats struct {
	TotalRequests   int     `json:"total_requests"`
	AvgCacheHitRate float64 `json:"avg_cache_hit_rate"`
	AvgTTFTMs       int64   `json:"avg_ttft_ms"`
	ActiveSessions  int     `json:"active_sessions"`
	ErrorCount      int     `json:"error_count"`
	PanicCount      int     `json:"panic_count"`
	TotalAccounts   int     `json:"total_accounts"`
	ActiveKeys      int     `json:"active_keys"`
}

func (s *SQLiteLogStore) Query(keyFilter, accountFilter, sessionFilter, agentFilter string, onlyErrors bool, limit int) ([]RequestLog, LogStats) {
	if s.db == nil {
		return []RequestLog{}, LogStats{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > 500 {
		limit = 100
	}

	// Calculate overall 24h stats
	statsQuery := `
	SELECT
		COUNT(*),
		COALESCE(AVG(CASE WHEN in_tokens > 0 THEN cache_hit_rate END), 0),
		COALESCE(AVG(CASE WHEN ttft_ms > 0 THEN ttft_ms END), 0),
		COUNT(DISTINCT CASE WHEN session_id != '-' AND session_id != '' THEN session_id END),
		SUM(CASE WHEN status >= 400 OR error != '' THEN 1 ELSE 0 END),
		SUM(CASE WHEN was_panicked = 1 THEN 1 ELSE 0 END)
	FROM request_logs;
	`
	var stats LogStats
	row := s.db.QueryRow(statsQuery)
	var avgCacheHit, avgTTFT float64
	var errCount, panicCount sql.NullInt64
	_ = row.Scan(&stats.TotalRequests, &avgCacheHit, &avgTTFT, &stats.ActiveSessions, &errCount, &panicCount)
	stats.AvgCacheHitRate = avgCacheHit
	stats.AvgTTFTMs = int64(avgTTFT)
	if errCount.Valid {
		stats.ErrorCount = int(errCount.Int64)
	}
	if panicCount.Valid {
		stats.PanicCount = int(panicCount.Int64)
	}

	// Build WHERE clause for filtered list
	var conditions []string
	var args []interface{}

	if keyFilter != "" {
		conditions = append(conditions, "(client_key LIKE ? OR key_masked LIKE ?)")
		args = append(args, "%"+keyFilter+"%", "%"+keyFilter+"%")
	}
	if accountFilter != "" {
		conditions = append(conditions, "account = ?")
		args = append(args, accountFilter)
	}
	if sessionFilter != "" {
		conditions = append(conditions, "session_id LIKE ?")
		args = append(args, "%"+sessionFilter+"%")
	}
	if agentFilter != "" {
		conditions = append(conditions, "(agent_key = ? OR agent_type = ? OR subagent_name = ?)")
		args = append(args, agentFilter, agentFilter, agentFilter)
	}
	if onlyErrors {
		conditions = append(conditions, "(status >= 400 OR error != '' OR was_panicked = 1)")
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE " + strings.Join(conditions, " AND ")
	}

	query := fmt.Sprintf(`
	SELECT
		id, timestamp, client_key, key_masked, account, session_id,
		agent_key, agent_type, subagent_name, instance_id, model,
		ttft_ms, total_dur_ms, in_tokens, out_tokens, cache_read_tokens,
		cache_hit_rate, tools_json, was_panicked, shrunk, error, status
	FROM request_logs
	%s
	ORDER BY timestamp DESC
	LIMIT ?;
	`, whereClause)
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return []RequestLog{}, stats
	}
	defer rows.Close()

	var logs []RequestLog
	for rows.Next() {
		var item RequestLog
		var toolsJSON string
		var wasPanickedInt, shrunkInt int
		err := rows.Scan(
			&item.ID, &item.Timestamp, &item.Key, &item.KeyMasked, &item.Account, &item.SessionID,
			&item.AgentKey, &item.AgentType, &item.SubagentName, &item.InstanceID, &item.Model,
			&item.TTFT, &item.TotalDur, &item.InTokens, &item.OutTokens, &item.CacheReadTokens,
			&item.CacheHitRate, &toolsJSON, &wasPanickedInt, &shrunkInt, &item.Error, &item.Status,
		)
		if err != nil {
			continue
		}
		item.WasPanicked = (wasPanickedInt == 1)
		item.Shrunk = (shrunkInt == 1)
		_ = json.Unmarshal([]byte(toolsJSON), &item.Tools)
		if item.Tools == nil {
			item.Tools = []string{}
		}
		logs = append(logs, item)
	}

	return logs, stats
}
