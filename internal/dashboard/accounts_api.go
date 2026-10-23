// Part of the dashboard package's port from superrelay (v1)'s ui.go —
// see register.go for the package doc comment. This file holds the
// account-pool-facing endpoints: list/delete/toggle/refresh-quotas/warmup.
package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"claudgravity/internal/accounts"
)

// accountsDashboardStaleAfter is how old an account's quota snapshot can get
// before a dashboard poll opportunistically refreshes it in the background.
// Deliberately NOT a standalone server-side ticker: nothing needs this data
// fresh except a human actually looking at the dashboard right now, and the
// dashboard already polls this endpoint every 15s on its own — piggybacking
// here means zero extra Google API calls while nobody has the page open.
const accountsDashboardStaleAfter = 2 * time.Minute

func handleAPIAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		email := r.URL.Query().Get("email")
		if email == "" {
			http.Error(w, `{"error":"missing required query param: email"}`, http.StatusBadRequest)
			return
		}
		if err := accounts.Manager().DeleteAccount(email); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"success":true,"email":%q}`, email)))
		return
	}

	list := accounts.Manager().GetAccounts()
	for _, acc := range list {
		accounts.Manager().TriggerAsyncQuotaRefresh(acc.Email, accountsDashboardStaleAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(list)
}

func handleAPIAccountToggle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	email := r.URL.Query().Get("email")
	disabledStr := r.URL.Query().Get("disabled")
	if email == "" || disabledStr == "" {
		http.Error(w, `{"error":"missing email or disabled parameter"}`, http.StatusBadRequest)
		return
	}
	disabled := (disabledStr == "true" || disabledStr == "1")
	if err := accounts.Manager().ToggleAccountDisabled(email, disabled); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(fmt.Sprintf(`{"success":true,"email":%q,"disabled":%v}`, email, disabled)))
}

func handleAPIRefreshQuotas(w http.ResponseWriter, r *http.Request) {
	go accounts.Manager().RefreshAllQuotas()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"status":"refresh_started"}`))
}

func handleAPIWarmup(w http.ResponseWriter, r *http.Request) {
	email := r.URL.Query().Get("email")
	if email == "" {
		http.Error(w, "missing email", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	msg, err := accounts.Manager().WarmupSingleAccount(ctx, email)
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": msg})
}

// handleAPIWarmupAll warms up every non-disabled account whose Gemini weekly
// window sits at 100% remaining (never activated) — the dashboard's single
// "warm up stale accounts" button, replacing the old per-row warmup button.
func handleAPIWarmupAll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	warmed, errs := accounts.Manager().WarmupAllFreshAccounts(ctx)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"warmed":  warmed,
		"errors":  errs,
		"count":   len(warmed),
	})
}
