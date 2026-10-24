// Part of the requestlog package's port from superrelay (v1)'s
// request_logger.go — see store.go for the split rationale. This file
// holds the session-oriented reads (QuerySessions/GetSessionDetail);
// query.go holds the flat, filtered request list (Query) and LogStats.
package requestlog

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// parseSQLiteTime handles Go's default time.Time.String() layout (with its
// optional " m=+<seconds>" monotonic-clock suffix) that MIN()/MAX()
// aggregates force scanned results through as a plain string — a direct
// db.Scan into time.Time handles this via the driver, but an aggregate
// doesn't, and that monotonic suffix isn't a layout time.Parse understands.
// Confirmed in v1: every one of these used to fail silently and fall
// through to the time.Now() fallback below, so every session's
// start_time/last_time was just "now".
func parseSQLiteTime(s string) time.Time {
	if idx := strings.Index(s, " m="); idx >= 0 {
		s = s[:idx]
	}

	formats := []string{
		"2006-01-02 15:04:05.999999999 -0700 MST",
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Now()
}

type SessionSummary struct {
	SessionID      string    `json:"session_id"`
	Key            string    `json:"key"`
	KeyMasked      string    `json:"key_masked"`
	Account        string    `json:"account"`
	StartTime      time.Time `json:"start_time"`
	LastTime       time.Time `json:"last_time"`
	TotalRequests  int       `json:"total_requests"`
	TotalInTokens  int64     `json:"total_in_tokens"`
	TotalOutTokens int64     `json:"total_out_tokens"`
	CacheHitRate   float64   `json:"cache_hit_rate"`
	MainRequests   int       `json:"main_requests"`
	SubRequests    int       `json:"sub_requests"`
	SubagentNames  []string  `json:"subagent_names"`
	HasErrors      bool      `json:"has_errors"`
	HasPanic       bool      `json:"has_panic"`
}

func (s *SQLiteLogStore) QuerySessions(limit int) []SessionSummary {
	if s.db == nil {
		return []SessionSummary{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if limit <= 0 || limit > 100 {
		limit = 50
	}

	query := `
	SELECT
		session_id,
		client_key,
		key_masked,
		account,
		MIN(timestamp) as start_time,
		MAX(timestamp) as last_time,
		COUNT(*) as total_requests,
		SUM(in_tokens) as total_in,
		SUM(out_tokens) as total_out,
		COALESCE(AVG(CASE WHEN in_tokens > 0 THEN cache_hit_rate END), 0) as avg_hit,
		SUM(CASE WHEN agent_type = 'main' THEN 1 ELSE 0 END) as main_count,
		SUM(CASE WHEN agent_type = 'subagent' THEN 1 ELSE 0 END) as sub_count,
		SUM(CASE WHEN status != 499 AND (status >= 400 OR error != '') THEN 1 ELSE 0 END) as err_count,
		SUM(CASE WHEN was_panicked = 1 THEN 1 ELSE 0 END) as panic_count,
		GROUP_CONCAT(DISTINCT CASE WHEN subagent_name != '' THEN subagent_name END) as subagent_names
	FROM request_logs
	WHERE session_id != '-' AND session_id != ''
	GROUP BY session_id
	ORDER BY last_time DESC
	LIMIT ?;
	`

	rows, err := s.db.Query(query, limit)
	if err != nil {
		return []SessionSummary{}
	}
	defer rows.Close()

	var list []SessionSummary = make([]SessionSummary, 0)
	for rows.Next() {
		var sm SessionSummary
		var totalIn, totalOut sql.NullInt64
		var avgHit sql.NullFloat64
		var mainCount, subCount, errCount, panicCount sql.NullInt64
		var startTimeStr, lastTimeStr string
		var subagentNamesRaw sql.NullString

		err := rows.Scan(
			&sm.SessionID, &sm.Key, &sm.KeyMasked, &sm.Account,
			&startTimeStr, &lastTimeStr,
			&sm.TotalRequests, &totalIn, &totalOut,
			&avgHit, &mainCount, &subCount,
			&errCount, &panicCount, &subagentNamesRaw,
		)
		if err != nil {
			continue
		}
		sm.StartTime = parseSQLiteTime(startTimeStr)
		sm.LastTime = parseSQLiteTime(lastTimeStr)
		if totalIn.Valid {
			sm.TotalInTokens = totalIn.Int64
		}
		if totalOut.Valid {
			sm.TotalOutTokens = totalOut.Int64
		}
		if avgHit.Valid {
			sm.CacheHitRate = avgHit.Float64
		}
		if mainCount.Valid {
			sm.MainRequests = int(mainCount.Int64)
		}
		if subCount.Valid {
			sm.SubRequests = int(subCount.Int64)
		}
		if errCount.Valid {
			sm.HasErrors = (errCount.Int64 > 0)
		}
		if panicCount.Valid {
			sm.HasPanic = (panicCount.Int64 > 0)
		}

		if subagentNamesRaw.Valid && subagentNamesRaw.String != "" {
			sm.SubagentNames = strings.Split(subagentNamesRaw.String, ",")
		}
		if sm.SubagentNames == nil {
			sm.SubagentNames = []string{}
		}

		list = append(list, sm)
	}

	return list
}

// GetSessionDetail returns the full picture for exactly one session: its
// aggregate summary (same shape as QuerySessions' rows) plus every request
// logged under it, in chronological (ascending) order — unlike Query(),
// which caps at 500 and sorts newest-first. Built for MCP-side verification
// of a specific session's behavior (model routing, cache hits, panics,
// errors), where silently truncating the timeline would hide exactly the
// failure being checked for.
func (s *SQLiteLogStore) GetSessionDetail(sessionID string) (*SessionSummary, []RequestLog) {
	if s.db == nil || sessionID == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	summaryQuery := `
	SELECT
		session_id,
		client_key,
		key_masked,
		account,
		MIN(timestamp) as start_time,
		MAX(timestamp) as last_time,
		COUNT(*) as total_requests,
		SUM(in_tokens) as total_in,
		SUM(out_tokens) as total_out,
		COALESCE(AVG(CASE WHEN in_tokens > 0 THEN cache_hit_rate END), 0) as avg_hit,
		SUM(CASE WHEN agent_type = 'main' THEN 1 ELSE 0 END) as main_count,
		SUM(CASE WHEN agent_type = 'subagent' THEN 1 ELSE 0 END) as sub_count,
		SUM(CASE WHEN status != 499 AND (status >= 400 OR error != '') THEN 1 ELSE 0 END) as err_count,
		SUM(CASE WHEN was_panicked = 1 THEN 1 ELSE 0 END) as panic_count
	FROM request_logs
	WHERE session_id = ?
	GROUP BY session_id;
	`

	var sm SessionSummary
	var totalIn, totalOut sql.NullInt64
	var avgHit sql.NullFloat64
	var mainCount, subCount, errCount, panicCount sql.NullInt64
	var startTimeStr, lastTimeStr string

	row := s.db.QueryRow(summaryQuery, sessionID)
	err := row.Scan(
		&sm.SessionID, &sm.Key, &sm.KeyMasked, &sm.Account,
		&startTimeStr, &lastTimeStr,
		&sm.TotalRequests, &totalIn, &totalOut,
		&avgHit, &mainCount, &subCount,
		&errCount, &panicCount,
	)
	if err != nil {
		return nil, nil // no rows for this session_id
	}
	sm.StartTime = parseSQLiteTime(startTimeStr)
	sm.LastTime = parseSQLiteTime(lastTimeStr)
	if totalIn.Valid {
		sm.TotalInTokens = totalIn.Int64
	}
	if totalOut.Valid {
		sm.TotalOutTokens = totalOut.Int64
	}
	if avgHit.Valid {
		sm.CacheHitRate = avgHit.Float64
	}
	if mainCount.Valid {
		sm.MainRequests = int(mainCount.Int64)
	}
	if subCount.Valid {
		sm.SubRequests = int(subCount.Int64)
	}
	if errCount.Valid {
		sm.HasErrors = errCount.Int64 > 0
	}
	if panicCount.Valid {
		sm.HasPanic = panicCount.Int64 > 0
	}

	subRows, err := s.db.Query(`SELECT DISTINCT subagent_name FROM request_logs WHERE session_id = ? AND subagent_name != ''`, sessionID)
	if err == nil {
		for subRows.Next() {
			var subName string
			if err := subRows.Scan(&subName); err == nil && subName != "" {
				sm.SubagentNames = append(sm.SubagentNames, subName)
			}
		}
		subRows.Close()
	}
	if sm.SubagentNames == nil {
		sm.SubagentNames = []string{}
	}

	detailQuery := `
	SELECT
		id, timestamp, client_key, key_masked, account, session_id,
		agent_key, agent_type, subagent_name, instance_id, model,
		ttft_ms, total_dur_ms, in_tokens, out_tokens, cache_read_tokens,
		cache_hit_rate, tools_json, was_panicked, shrunk, error, status
	FROM request_logs
	WHERE session_id = ?
	ORDER BY timestamp ASC
	LIMIT 2000;
	`
	rows, err := s.db.Query(detailQuery, sessionID)
	if err != nil {
		return &sm, []RequestLog{}
	}
	defer rows.Close()

	requests := make([]RequestLog, 0)
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
		item.WasPanicked = wasPanickedInt == 1
		item.Shrunk = shrunkInt == 1
		_ = json.Unmarshal([]byte(toolsJSON), &item.Tools)
		if item.Tools == nil {
			item.Tools = []string{}
		}
		requests = append(requests, item)
	}

	return &sm, requests
}
