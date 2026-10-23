package clistubs

import (
	"encoding/json"
	"net/http"
	"time"

	"claudgravity/internal/ratelimit"
)

// handleUsage answers GET /api/oauth/usage — GAP-CS-01: provides full
// usage structures expected by Claude Code CLI v2.1+ (/usage command).
func handleUsage(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	resets5h := now.Add(5 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	resets7d := now.Add(7 * 24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")

	fiveHourWindow := map[string]interface{}{
		"utilization":       0.05,
		"resets_at":         resets5h,
		"limit_dollars":     nil,
		"used_dollars":      nil,
		"remaining_dollars": nil,
		"locked_reason":     nil,
	}
	weeklyWindow := map[string]interface{}{
		"utilization":       0.05,
		"resets_at":         resets7d,
		"limit_dollars":     nil,
		"used_dollars":      nil,
		"remaining_dollars": nil,
		"locked_reason":     nil,
	}

	limits := []map[string]interface{}{
		{
			"kind":      "session",
			"group":     "session",
			"percent":   0.05,
			"severity":  "normal",
			"resets_at": resets5h,
			"scope":     nil,
			"is_active": true,
		},
		{
			"kind":      "weekly_all",
			"group":     "weekly",
			"percent":   0.05,
			"severity":  "normal",
			"resets_at": resets7d,
			"scope":     nil,
			"is_active": false,
		},
		{
			"kind":      "weekly_scoped",
			"group":     "weekly",
			"percent":   0.05,
			"severity":  "normal",
			"resets_at": resets7d,
			"scope": map[string]interface{}{
				"model": map[string]interface{}{
					"display_name": "Opus",
				},
			},
			"is_active": false,
		},
		{
			"kind":      "weekly_scoped",
			"group":     "weekly",
			"percent":   0.05,
			"severity":  "normal",
			"resets_at": resets7d,
			"scope": map[string]interface{}{
				"model": map[string]interface{}{
					"display_name": "Sonnet",
				},
			},
			"is_active": false,
		},
	}

	resp := map[string]interface{}{
		"five_hour":            fiveHourWindow,
		"seven_day":            weeklyWindow,
		"seven_day_oauth_apps": nil,
		"seven_day_opus":       weeklyWindow,
		"seven_day_sonnet":     weeklyWindow,
		"extra_usage": map[string]interface{}{
			"is_enabled":           false,
			"monthly_limit":        0,
			"used_credits":         0,
			"utilization":          0,
			"currency":             "USD",
			"decimal_places":       nil,
			"disabled_reason":      nil,
			"user_disabled":        false,
			"spend_limit_reached":  false,
			"credits_ever_enabled": false,
			"daily":                nil,
			"weekly":               nil,
		},
		"limits": limits,
		"spend": map[string]interface{}{
			"used":                 map[string]interface{}{"amount_minor": 0, "currency": "USD", "exponent": 2},
			"limit":                nil,
			"percent":              0,
			"severity":             "normal",
			"enabled":              false,
			"disabled_reason":      nil,
			"cap":                  nil,
			"balance":              nil,
			"auto_reload":          nil,
			"can_purchase_credits": false,
			"can_toggle":           false,
		},
	}

	ratelimit.ApplyHeaders(w.Header(), nil, "claude-opus-5-5")
	b, _ := json.Marshal(resp)
	writeJSON(w, r, string(b))
}
