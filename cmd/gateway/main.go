// main is the gateway's entry point. By the rule in Part 4.3 of
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md, this file stays
// thin forever: flags, wiring, InitXxx() calls, starting the HTTP server —
// any branch on domain data here is a placement bug, not a style nit.
//
// This is the first REAL entry point — Phase 0's skeleton just printed and
// exited. It wires: config -> logger -> account manager -> model catalog ->
// internal/httpapi.NewRouter -> plain HTTP listener (unchanged — still the
// one serving real live Claude Code traffic via launch_v2.cmd) plus, as of
// this step, the MITM CA / CONNECT-tunnel listener / TLS API listener
// (cmd/gateway/mitmlisteners.go — see its own doc comment for exact scope
// and the one deliberate divergence from v1's wiring), then waits for
// SIGINT/SIGTERM for a graceful shutdown of every listener it started.
//
// internal/dashboard.RegisterUIHandlers is now mounted too, composed onto
// the same top-level mux as httpapi's router (httpapi only ever registers
// under "/v1/", so there's no collision with dashboard owning "/").
//
// internal/warmup's periodic account-warming goroutine, the third item
// this file used to list as deferred, turned out NOT to exist in v1 at
// all on inspection: superrelay (v1)'s server.go/main() has no ticker or
// background goroutine that ever calls WarmupAllFreshAccounts/
// WarmupSingleAccount — grepping v1's whole source tree, the only call
// sites are ui.go's handleAPIWarmup/handleAPIWarmupAll, i.e. the
// dashboard's manual "warm up stale accounts" button (selector.go even
// says so explicitly in its own comment: "RefreshAllQuotas only runs off
// a manual dashboard [trigger]", and the sibling quota-refresh path is the
// same way). Inventing a periodic trigger here that v1 never had would be
// exactly the kind of unverified guess this migration's ground rules rule
// out — internal/accounts/warmup.go (ported by the parallel dashboard
// work, since these are *AccountManager methods) already has
// WarmupAllFreshAccounts/WarmupSingleAccount ready to call; the dashboard
// agent's own "warm up stale accounts" button wiring is the correct,
// faithful place to trigger them, not a fabricated ticker in main.go. See
// ROADMAP_PROGRESS.md for the full writeup of this finding.
//
// Running this now against a real config.json and sending it one
// /v1/messages request is exactly the "shadow observation" Phase 5 opens
// with — see ROADMAP_PROGRESS.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/accounts/tiering"
	"claudgravity/internal/clistubs"
	"claudgravity/internal/config"
	"claudgravity/internal/dashboard"
	"claudgravity/internal/httpapi"
	"claudgravity/internal/logx"
	"claudgravity/internal/mitm"
	"claudgravity/internal/requestlog"
	"claudgravity/internal/speccapture"
	"claudgravity/internal/tlsfingerprint"
)

func main() {
	configPath := flag.String("config", "config.json", "path to config.json")
	authDir := flag.String("auths", "auths", "path to the accounts directory")
	// caDir is intentionally its OWN directory under ClaudGravityNext, never
	// pointed at superrelay (v1)'s "certs" directory: this is a parallel test
	// instance, not yet a replacement, and the architecture spec's Part 9.1
	// is explicit that an existing, already-trusted MITM CA must never be
	// regenerated/touched by a second process. A fresh v2 CA means a client
	// that wants to trust THIS gateway over CONNECT/TLS needs to install v2's
	// own cert — expected and fine for a test instance, not yet true blue-green.
	caDir := flag.String("ca-dir", "ca", "directory for this gateway's own MITM CA certificate/key (separate from superrelay's \"certs\" dir — never shared)")
	// connectPort is deliberately its OWN flag/port, not reused from
	// serverCfg.Port: v1 shares one port between its plain API and its
	// CONNECT-tunnel proxy (server.go's rootHandler branches on
	// r.Method==CONNECT); giving the tunnel a separate port here means
	// wiring it in cannot change the live plain-HTTP listener's behavior.
	// 0 (the default) means "serverCfg.Port + 1", resolved after config
	// loads below.
	connectPort := flag.Int("connect-port", 0, "port for the CONNECT-tunnel proxy (HTTPS_PROXY target); 0 means server.port+1")
	allowPassthrough := flag.Bool("allow-passthrough", false, "forward unmapped paths on the plain listener to the real Anthropic API (credentials are scrubbed); default: answer 404")
	flag.Parse()

	cfgMgr, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ClaudGravityNext: failed to load config %s: %v\n", *configPath, err)
		os.Exit(1)
	}

	logx.InitLogger("gateway.log")
	installSecretMatcher(cfgMgr)

	serverCfg := cfgMgr.GetServerConfig()
	speccapture.InitSpecCapture(serverCfg.SpecCaptureIsEnabled(), serverCfg.SpecCaptureDir, serverCfg.SpecCaptureRetentionDays)

	// Found live: real traffic was flowing (confirmed via gateway.log) but
	// no session ever showed up in the dashboard. internal/requestlog.Add
	// (called from every request path) silently no-ops when its
	// SQLiteLogStore.db is nil — InitLogStore was simply never called
	// anywhere in this binary. "" uses the package's own default filename
	// ("superrelay_logs.db", matching v1's convention — a different file
	// in this binary's own working directory, never shared with v1's).
	if err := requestlog.InitLogStore(""); err != nil {
		logx.LogGateway("[ClaudGravityNext] Warning: request log store init failed (dashboard sessions/logs will stay empty): %v", err)
	}

	tlsfingerprint.SetMode(serverCfg.TLSFingerprint)
	logx.LogGateway("[ClaudGravityNext] Google-bound TLS ClientHello: %s", tlsfingerprint.Mode())
	if len(serverCfg.ModelOverrides) > 0 {
		skipped := tiering.SetFamilyOverrides(serverCfg.ModelOverrides)
		logx.LogGateway("[ClaudGravityNext] Model overrides: %v (ignored: %v)", serverCfg.ModelOverrides, skipped)
	}

	if err := accounts.InitAccountManager(*authDir, serverCfg); err != nil {
		logx.LogGateway("[ClaudGravityNext] Notice: auths dir init: %v", err)
	}

	// Conversation → account bindings survive a restart (they keep each
	// conversation on the account that holds its prompt cache).
	affinityPath := filepath.Join(filepath.Dir(*authDir), "session_affinity.json")
	accounts.GlobalSelector().StartAffinityPersistence(affinityPath, 10*time.Second)

	// Kick off an initial background quota refresh for all accounts in the pool
	// so the dashboard and the account selector immediately have real, fresh
	// Google quota numbers and accurate cooldown states from startup.
	go accounts.Manager().RefreshAllQuotasStaggered(2*time.Second, 6*time.Second)

	if _, err := accounts.Manager().FetchAvailableModels(false); err != nil {
		logx.LogGateway("[ClaudGravityNext] Warning: initial model catalog fetch failed (retrying in the background): %v", err)
		go retryModelCatalog()
	} else {
		logx.LogGateway("[ClaudGravityNext] Model catalog loaded (context window / output ceilings)")
	}

	// Composed, not nested: httpapi.NewRouter only ever registers under
	// "/v1/" (verified by reading router.go — no catch-all of its own), so
	// mounting it on a plain top-level mux alongside dashboard's routes
	// (which own "/", "/api/*", "/oauth/*", "/ws") cannot collide — every
	// request matches whichever pattern is more specific, standard
	// http.ServeMux semantics, no custom fallback logic needed.
	topMux := http.NewServeMux()
	// GAP-P0-02: register the CLI control-endpoint stubs (account/org
	// identity, settings, policy limits, telemetry, cache_touch) and
	// /v1/ API routes.
	// CommonAPIMiddleware is strictly applied to CLI stubs and httpapi.NewRouter
	// routes; dashboard routes are excluded to prevent wildcard CORS on sensitive endpoints.
	router := httpapi.NewRouter(cfgMgr)
	clistubs.RegisterHandlers(topMux, httpapi.CommonAPIMiddleware)
	topMux.Handle("/v1/", router)
	topMux.Handle("/count_tokens", router)
	topMux.Handle("/count_tokens/", router)
	// Mount dashboard routes (/dashboard, /dashboard/, /ws, /api/*, /oauth/*)
	// without binding the catch-all "/".
	dashboard.RegisterDashboardRoutes(topMux, cfgMgr)

	// Root fallback: direct browser visits to "/" redirect to "/dashboard"
	// (or passthrough if the request was an API request or addressed to an Anthropic host).
	// Unmatched requests on other paths fall through to mitm.PassthroughToReal
	// rather than returning a 401 Basic Auth challenge.
	topMux.HandleFunc("/", newRootFallback(*allowPassthrough))
	// RecoverMiddleware guards the top level against any unhandled panics across all routes.
	var apiHandler http.Handler = httpapi.RecoverMiddleware(topMux)

	// Intercepted listener mux (for CONNECT tunnel and TLS API listener).
	// Routes /v1/* and CLI control stubs locally, and falls through to
	// mitm.PassthroughToReal for any unhandled endpoint so unknown Anthropic
	// API calls reach the real servers instead of hitting dashboard Basic Auth.
	interceptedMux := http.NewServeMux()
	clistubs.RegisterHandlers(interceptedMux, httpapi.CommonAPIMiddleware)
	interceptedMux.Handle("/v1/", router)
	interceptedMux.Handle("/count_tokens", router)
	interceptedMux.Handle("/count_tokens/", router)
	interceptedMux.HandleFunc("/", mitm.PassthroughToReal)
	var mitmHandler http.Handler = httpapi.RecoverMiddleware(interceptedMux)

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", serverCfg.Port),
		Handler:           apiHandler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// MITM CA + CONNECT-tunnel + TLS API listener: additional goroutines
	// alongside srv above, never replacing it. resolvedConnectPort applies
	// the "-connect-port=0 means server.port+1" default documented on the
	// flag itself. mitmSrvs is nil (both fields) if InitMitmCA failed —
	// startMitmListeners logs that as a warning and the plain HTTP
	// listener this binary already serves real traffic on is completely
	// unaffected either way.
	resolvedConnectPort := *connectPort
	if resolvedConnectPort == 0 {
		resolvedConnectPort = serverCfg.Port + 1
	}
	// Record the resolved port BEFORE starting the listener (not after) so
	// a request to /api/bootstrap arriving while the listener is still
	// coming up — or even if it fails to bind — reports the port this
	// process actually intends to use, rather than racing dashboard
	// requests against startMitmListeners's own goroutines.
	cfgMgr.SetConnectPort(resolvedConnectPort)
	mitmSrvs, _ := startMitmListeners(cfgMgr, *caDir, resolvedConnectPort, mitmHandler)

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		<-sigChan
		logx.LogGateway("[ClaudGravityNext] Shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		if mitmSrvs != nil {
			if mitmSrvs.connect != nil {
				_ = mitmSrvs.connect.Shutdown(ctx)
			}
			if mitmSrvs.tls != nil {
				_ = mitmSrvs.tls.Shutdown(ctx)
			}
		}
		accounts.GlobalSelector().SaveAffinity(affinityPath)
		requestlog.Close()
	}()

	logx.LogGateway("[ClaudGravityNext] Listening on http://localhost:%d (plain API + dashboard)", serverCfg.Port)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logx.LogGateway("[ClaudGravityNext] Server error: %v", err)
	}
}
