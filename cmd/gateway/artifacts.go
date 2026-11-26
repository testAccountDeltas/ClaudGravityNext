package main

import (
	"net/http"
	"strings"

	"claudgravity/internal/artifacts"
	"claudgravity/internal/config"
	"claudgravity/internal/dashboard"
	"claudgravity/internal/httpapi"
	"claudgravity/internal/logx"
)

// mountArtifacts opens the artifact store and registers:
//   - the Frame/MCP API routes (/api/frame/*, /api/artifacts/mcp) on every mux
//   - the sandboxed browser viewer (/code/artifact/*, /artifact/*) on every mux
//   - the dashboard list/delete API (/api/dashboard/artifacts*) on topMux only
func mountArtifacts(cfgMgr *config.ConfigManager, topMux, interceptedMux *http.ServeMux) {
	_ = mountArtifactsAt(cfgMgr, artifacts.DefaultDBPath, topMux, interceptedMux)
}

func mountArtifactsAt(cfgMgr *config.ConfigManager, dbPath string, topMux, interceptedMux *http.ServeMux) *artifacts.Store {
	store, err := artifacts.Open(dbPath)
	if err != nil {
		logx.LogGateway("[ClaudGravityNext] Warning: artifacts store unavailable (Artifact tool will not work): %v", err)
		return nil
	}
	h := artifacts.NewHandler(store, func(r *http.Request) bool {
		return artifactsCallerAllowed(cfgMgr, r)
	})
	for _, m := range []*http.ServeMux{topMux, interceptedMux} {
		if m == nil {
			continue
		}
		h.Register(m, httpapi.CommonAPIMiddleware)
		h.RegisterViewer(m)
	}
	if topMux != nil {
		h.RegisterDashboardAPI(topMux, func(next http.HandlerFunc) http.HandlerFunc {
			return dashboard.RequireAuth(cfgMgr, next)
		})
	}
	return store
}

// artifactsCallerAllowed accepts the same credentials the /v1 API does: a
// tenant key as a Bearer token or x-api-key.
func artifactsCallerAllowed(cfgMgr *config.ConfigManager, r *http.Request) bool {
	key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if key == "" {
		key = strings.TrimSpace(r.Header.Get("x-api-key"))
	}
	_, ok := cfgMgr.GetTenant(key)
	return ok
}
