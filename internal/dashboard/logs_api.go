// Part of the dashboard package's port from superrelay (v1)'s ui.go —
// see register.go for the package doc comment. This file holds the
// request-log/session-history endpoints.
package dashboard

import (
	"encoding/json"
	"net/http"
	"strconv"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
	"claudgravity/internal/requestlog"
)

func handleAPILogs(cfgMgr *config.ConfigManager, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		requestlog.Clear()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"cleared"}`))
		return
	}

	key := r.URL.Query().Get("key")
	account := r.URL.Query().Get("account")
	session := r.URL.Query().Get("session")
	agent := r.URL.Query().Get("agent")
	onlyErrors := r.URL.Query().Get("errors") == "true" || r.URL.Query().Get("errors") == "1"
	limitStr := r.URL.Query().Get("limit")

	limit := 100
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}

	logs, stats := requestlog.Query(key, account, session, agent, onlyErrors, limit)

	list := accounts.Manager().GetAccounts()
	tenants := cfgMgr.GetAllTenants()
	stats.TotalAccounts = len(list)
	stats.ActiveKeys = len(tenants)

	resp := map[string]interface{}{
		"logs":  logs,
		"stats": stats,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func handleAPISessions(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	sessions := requestlog.QuerySessions(limit)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sessions)
}

// handleAPISessionDetail returns the full timeline for exactly one session
// (GET /api/session?id=<session_id>): aggregate summary plus every request
// logged under it, oldest first. Built for MCP-side verification that a
// given session actually routed/behaved as expected — /api/logs's 500-row
// cap and newest-first order make it unsuitable for checking a specific
// session's complete, in-order behavior.
func handleAPISessionDetail(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	w.Header().Set("Content-Type", "application/json")
	if id == "" {
		http.Error(w, `{"error":"missing required query param: id"}`, http.StatusBadRequest)
		return
	}

	summary, requests := requestlog.GetSessionDetail(id)
	if summary == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error":      "no logged requests for this session_id",
			"session_id": id,
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"summary":  summary,
		"requests": requests,
	})
}
