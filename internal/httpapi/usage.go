// Part of the httpapi package — see errors.go for the package doc
// comment. GET /v1/usage — a faithful port of v1's handleUsage. Reads
// quota data straight from internal/accounts' Record.Quotas; does not
// recompute any of internal/ratelimit's five-hour/weekly bucket-picking
// logic (that's for the response HEADERS internal/orchestrate/
// internal/ratelimit already set on every request — this endpoint's own
// JSON body reports every quota group/bucket the account has, not just
// the two "representative" ones).
package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
)

type usageBucketOut struct {
	Window           string  `json:"window"`
	DisplayName      string  `json:"display_name"`
	RemainingPercent float64 `json:"remaining_percent"`
	ResetTime        string  `json:"reset_time"`
	Description      string  `json:"description"`
}

type usageGroupOut struct {
	DisplayName string           `json:"display_name"`
	Description string           `json:"description"`
	Buckets     []usageBucketOut `json:"buckets"`
}

func handleUsage(cfgMgr *config.ConfigManager, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		WriteAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "Method Not Allowed")
		return
	}
	tenant, ok := authenticatedTenantFromRequest(cfgMgr, r)
	if !ok {
		WriteAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid or missing API key")
		return
	}
	clientKey := r.Header.Get("x-api-key")
	if clientKey == "" {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			clientKey = strings.TrimSpace(authHeader[7:])
		} else {
			clientKey = authHeader
		}
	}

	convAnchor := r.URL.Query().Get("conversation")
	if convAnchor == "" {
		// Claude Code's own CLI identifies a session via the
		// x-claude-code-session-id HEADER on /v1/messages, not a query
		// string on this GET endpoint — without this, a usage query for a
		// specific session fell straight through to the generic
		// per-API-key sticky fallback below, returning the WRONG
		// account's quota data.
		if headerSessionID := r.Header.Get("x-claude-code-session-id"); headerSessionID != "" {
			convAnchor = accounts.DeriveConversationAnchor(nil, headerSessionID, "")
		}
	}
	email := ""
	if convAnchor != "" {
		email = accounts.GlobalSelector().GetAffinityAccount(convAnchor)
	}
	if email == "" && tenant.StickyAccount != "" {
		email = tenant.StickyAccount
	}

	var acc *accounts.Record
	if email != "" {
		acc, _ = accounts.Manager().GetAccount(email)
	}
	if acc == nil {
		acc = accounts.GlobalSelector().GetOrAssignStickyAccount(clientKey, tenant)
	}

	// A tenant with google_auth_type "api_key" has no OAuth account pool
	// at all (acc is nil) — report a stable synthetic low-utilization
	// number instead of 404ing outright.
	if acc == nil {
		now := time.Now()
		resp := map[string]interface{}{
			"account":        "",
			"project_id":     "",
			"cooldown_until": nil,
			"last_checked":   now,
			"groups": []usageGroupOut{{
				DisplayName: "Usage",
				Description: "This tenant authenticates with a direct API key, not a Google OAuth account pool — no real quota data to report.",
				Buckets: []usageBucketOut{
					{Window: "5h", DisplayName: "5-Hour", RemainingPercent: 95.0, ResetTime: now.Add(5 * time.Hour).UTC().Format(time.RFC3339), Description: "Synthetic — no OAuth account pool"},
					{Window: "7d", DisplayName: "Weekly", RemainingPercent: 95.0, ResetTime: now.Add(7 * 24 * time.Hour).UTC().Format(time.RFC3339), Description: "Synthetic — no OAuth account pool"},
				},
			}},
		}
		b, _ := json.MarshalIndent(resp, "", "  ")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
		return
	}

	// Only block on a real network round-trip the first time this
	// account has no quota data at all — re-fetching synchronously on
	// every poll of this endpoint would hang the response for however
	// long Google's quota call takes. Serve the cached snapshot and let
	// the usual staleness-triggered async refresh catch it up.
	if len(acc.Quotas) == 0 {
		_ = accounts.Manager().FetchQuota(acc.Email)
		if fresh, ok := accounts.Manager().GetAccount(acc.Email); ok {
			acc = fresh
		}
	} else {
		accounts.Manager().TriggerAsyncQuotaRefresh(acc.Email, 45*time.Second)
	}

	var groupsOut []usageGroupOut
	for _, g := range acc.Quotas {
		var bList []usageBucketOut
		for _, b := range g.Buckets {
			pct := float64(int(b.RemainingFraction*1000+0.5)) / 10.0
			bList = append(bList, usageBucketOut{
				Window:           b.Window,
				DisplayName:      b.DisplayName,
				RemainingPercent: pct,
				ResetTime:        b.ResetTime,
				Description:      b.Description,
			})
		}
		groupsOut = append(groupsOut, usageGroupOut{
			DisplayName: g.DisplayName,
			Description: g.Description,
			Buckets:     bList,
		})
	}

	resp := map[string]interface{}{
		"account":        acc.Email,
		"project_id":     acc.ProjectID,
		"cooldown_until": acc.CooldownUntil,
		"last_checked":   acc.LastChecked,
		"groups":         groupsOut,
	}

	b, _ := json.MarshalIndent(resp, "", "  ")
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}
