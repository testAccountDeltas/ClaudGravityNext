// Package dashboard is a port of superrelay (v1)'s ws_hub.go + ui.go.
//
// wshub.go/wsconn.go (the WSHub/WSClient connection hub) were ported in
// Phase 1 of this migration — pure infrastructure with no dependency on
// anything account-shaped, see that file's own doc comment.
//
// ui.go (RegisterUIHandlers and every handleAPI*/handleOAuth* function,
// this file plus accounts_api.go/logs_api.go/keys_api.go/oauth_api.go/
// bootstrap_api.go/page.go/auth.go) was deliberately left ENTIRELY
// unported in Phase 1 — see ROADMAP_PROGRESS.md's Phase 1 entry for
// ui.go for the full reasoning. Short version: 9 of its 15 functions
// called internal/accounts' *AccountManager directly, which didn't exist
// yet (Phase 2); RegisterUIHandlers is the one function that actually
// wires all 15 into a working dashboard, so porting the other 6 alone
// would have been partial-port theater, not progress. internal/accounts,
// internal/httpapi and internal/orchestrate all exist and are live now
// (cmd/gateway/main.go serves real traffic against real Google accounts)
// — this file is that deferral's documented follow-through, not new scope.
//
// Split across 8 files by responsibility, same discipline as every other
// package this migration: register.go (this file — RegisterUIHandlers),
// auth.go (requireDashboardAuth), page.go (handleDashboard +
// dashboard.html embed), accounts_api.go, logs_api.go, keys_api.go,
// oauth_api.go, bootstrap_api.go.
//
// Two functions v1's WarmupSingleAccount/WarmupAllFreshAccounts needed on
// *AccountManager were THEMSELVES still deferred as of this file's port
// (see internal/warmup/errors.go's doc comment) — closed in
// internal/accounts/warmup.go alongside this port, for the same reason:
// they need direct access to am.accounts (unexported), so they belong on
// AccountManager itself, not as free functions in internal/warmup.
package dashboard

import (
	"net/http"
	"strings"

	"golang.org/x/net/websocket"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
	"claudgravity/internal/mitm"
	"claudgravity/internal/requestlog"
)

// RegisterUIHandlers mounts the dashboard (status page, its /api/*
// management endpoints, /oauth/* login flow, and /ws for live updates)
// onto mux. Mirrors v1's ui.go RegisterUIHandlers route-for-route.
//
// Also self-wires globalWSHub as the Broadcaster for internal/accounts
// and internal/requestlog (their own SetBroadcaster was never called
// anywhere — found live: real traffic was flowing and being logged via
// requestlog.Add, but every session silently vanished since Add's
// db==nil guard only explains an uninitialized STORE, not an unwired
// broadcaster; the two are separate gaps, both closed here). This is the
// one place that already owns globalWSHub and is already the
// composition boundary between "dashboard exists" and "accounts/
// requestlog exist" — cmd/gateway/main.go calling a bare SetBroadcaster
// pair would just be this same wiring moved one file up for no benefit,
// and main.go would need an exported Hub() accessor it doesn't otherwise
// need for anything else.
// RegisterDashboardRoutes mounts the dashboard routes (/dashboard, /dashboard/,
// /api/* management endpoints, /oauth/* login flow, and /ws for live updates)
// onto mux, WITHOUT binding the catch-all "/". This allows the top-level router
// or gateway to handle unmatched endpoints independently (e.g. falling through
// to MITM passthrough) without triggering dashboard Basic Auth challenges.
func RegisterDashboardRoutes(mux *http.ServeMux, cfgMgr *config.ConfigManager) {
	accounts.SetBroadcaster(globalWSHub)
	requestlog.SetBroadcaster(globalWSHub)

	mux.HandleFunc("/dashboard", requireDashboardAuth(cfgMgr, handleDashboard))
	mux.HandleFunc("/dashboard/", requireDashboardAuth(cfgMgr, handleDashboard))
	mux.Handle("/ws", requireDashboardAuth(cfgMgr, func(w http.ResponseWriter, r *http.Request) {
		websocket.Handler(HandleWS).ServeHTTP(w, r)
	}))
	mux.HandleFunc("/api/accounts", requireDashboardAuth(cfgMgr, handleAPIAccounts))
	mux.HandleFunc("/api/accounts/toggle", requireDashboardAuth(cfgMgr, handleAPIAccountToggle))
	mux.HandleFunc("/api/refresh-quotas", requireDashboardAuth(cfgMgr, handleAPIRefreshQuotas))
	mux.HandleFunc("/api/warmup", requireDashboardAuth(cfgMgr, handleAPIWarmup))
	mux.HandleFunc("/api/warmup-all", requireDashboardAuth(cfgMgr, handleAPIWarmupAll))
	mux.HandleFunc("/api/logs", requireDashboardAuth(cfgMgr, func(w http.ResponseWriter, r *http.Request) {
		handleAPILogs(cfgMgr, w, r)
	}))
	mux.HandleFunc("/api/sessions", requireDashboardAuth(cfgMgr, handleAPISessions))
	mux.HandleFunc("/api/session", requireDashboardAuth(cfgMgr, handleAPISessionDetail))
	mux.HandleFunc("/api/model-catalog", requireDashboardAuth(cfgMgr, handleAPIModelCatalog))
	mux.HandleFunc("/api/keys", requireDashboardAuth(cfgMgr, func(w http.ResponseWriter, r *http.Request) {
		handleAPIKeys(cfgMgr, w, r)
	}))
	mux.HandleFunc("/oauth/login", requireDashboardAuth(cfgMgr, handleOAuthLogin))
	mux.HandleFunc("/oauth/callback", requireDashboardAuth(cfgMgr, handleOAuthCallback))

	// /api/bootstrap is deliberately NOT behind requireDashboardAuth: the
	// installer scripts (install.ps1/install.sh) only ever have the end
	// user's own tenant key, never the admin dashboard password. It's
	// gated on that tenant key instead (see handleAPIBootstrap) so a
	// random caller without a valid key can't scrape proxy_secret/CA PEM.
	mux.HandleFunc("/api/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		handleAPIBootstrap(cfgMgr, w, r)
	})
}

// RegisterUIHandlers mounts the dashboard routes onto mux, including the catch-all
// "/" handler for backwards compatibility with v1. For API requests (or when
// Authorization starts with Bearer sk- or x-api-key is present or on unmapped paths),
// requests fall through to mitm.PassthroughToReal rather than prompting for dashboard auth.
func RegisterUIHandlers(mux *http.ServeMux, cfgMgr *config.ConfigManager) {
	RegisterDashboardRoutes(mux, cfgMgr)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		isAPI := mitm.IsAnthropicHost(r.Host) ||
			r.Header.Get("x-api-key") != "" ||
			strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-") ||
			r.Header.Get("anthropic-version") != ""
		if r.URL.Path != "/" || isAPI {
			mitm.PassthroughToReal(w, r)
			return
		}
		requireDashboardAuth(cfgMgr, handleDashboard)(w, r)
	})
}
