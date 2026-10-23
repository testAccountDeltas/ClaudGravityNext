// Part of the dashboard package's port from superrelay (v1)'s ui.go —
// see register.go for the package doc comment. This file holds the
// tenant API-key lifecycle endpoint and the model-catalog read endpoint.
package dashboard

import (
	"encoding/json"
	"net/http"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
	"claudgravity/internal/requestlog"
)

// handleAPIModelCatalog exposes the real per-model limits (context window,
// max output tokens, thinking budget) fetched from Google's own CloudCode
// API — used by internal/translate/internal/tokens for clamping/truncation.
func handleAPIModelCatalog(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(accounts.ModelCatalog().GetAll())
}

func handleAPIKeys(cfgMgr *config.ConfigManager, w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var req struct {
			Name          string `json:"name"`
			StickyAccount string `json:"sticky_account"`
			Strategy      string `json:"strategy"`
			Model         string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		key, err := cfgMgr.CreateAPIKey(req.Name, req.StickyAccount, req.Strategy, req.Model)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"key": key, "name": req.Name})
		return
	}

	if r.Method == http.MethodDelete {
		key := r.URL.Query().Get("key")
		deleted := false
		for k := range cfgMgr.GetAllTenants() {
			if k == key || requestlog.MaskKey(k) == key {
				_ = cfgMgr.DeleteAPIKey(k)
				deleted = true
				break
			}
		}
		if !deleted {
			_ = cfgMgr.DeleteAPIKey(key)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"deleted"}`))
		return
	}

	tenants := cfgMgr.GetAllTenants()
	masked := make(map[string]config.TenantConfig, len(tenants))
	for k, v := range tenants {
		masked[requestlog.MaskKey(k)] = v
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(masked)
}
