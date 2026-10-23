// Part of cmd/gateway — kept out of main.go per Part 4.3's file-size
// discipline. Wires the two listeners superrelay (v1)'s server.go starts
// alongside its plain API port: a CONNECT-tunnel proxy (HandleConnectTunnel)
// and a TLS API listener using a leaf cert minted by the MITM CA. Both are
// genuinely new to this migration — nothing in Phases 0-5 built the
// equivalent of v1's interceptedMux/HandleConnectTunnel wiring before now
// (see ROADMAP_PROGRESS.md's Phase 3 entry for cmd/gateway/main.go, which
// documented this as explicitly deferred).
//
// Deliberate divergence from v1's actual wiring (documented, not
// accidental): v1 handles CONNECT on the SAME http.Server/port as its
// plain API (server.go's rootHandler checks r.Method == http.MethodConnect
// before falling through to apiMux). This file gives the CONNECT tunnel
// its OWN port instead, specifically so wiring it in cannot change the
// live-traffic behavior of the existing plain HTTP listener on
// serverCfg.Port — main.go's own instructions for this step require that
// listener's behavior stay byte-for-byte unchanged. The non-CONNECT
// fallback on the new port mirrors v1's rootHandler shape anyway (serves
// the same api handler), so nothing is lost, just isolated onto its own
// port.
//
// GAP-P0-01 resolved: intercepted handler layers CLI-identity-spoofing
// endpoints (clistubs) and a PassthroughToReal("/") catch-all on top of
// /v1/* routes. The CONNECT tunnel and TLS listener receive this handler
// (mitmHandler from main.go), so any unmapped Anthropic API endpoint
// transparently passes through to the real api.anthropic.com/claude.ai
// instead of hitting dashboard Basic Auth.
package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"
	"time"

	"claudgravity/internal/config"
	"claudgravity/internal/logx"
	"claudgravity/internal/mitm"
)

// mitmListeners bundles the two optional servers startMitmListeners may
// start, so main.go has something uniform to Shutdown() regardless of
// which (if either) actually came up.
type mitmListeners struct {
	connect *http.Server
	tls     *http.Server
}

// startMitmListeners mints/loads the MITM CA under caDir (v2's OWN
// directory — see cmd/gateway/main.go's flag doc comment for why this is
// never pointed at superrelay's live "certs" directory) and, if that
// succeeds, starts the CONNECT-tunnel proxy and TLS API listener as
// additional goroutines. apiHandler is the same httpapi.NewRouter(cfgMgr)
// handler already serving the plain HTTP listener — reused here exactly as
// the migration step instructed, not rebuilt.
//
// Returns the servers that actually started (for graceful shutdown) and
// the initialized CA (nil on failure — both listeners are then skipped,
// logged as warnings, never fatal: the plain HTTP listener this binary
// already serves real traffic on must keep working regardless).
func startMitmListeners(cfgMgr *config.ConfigManager, caDir string, connectPort int, apiHandler http.Handler) (*mitmListeners, *mitm.MitmCA) {
	serverCfg := cfgMgr.GetServerConfig()

	mitmCA, err := mitm.InitMitmCA(caDir)
	if err != nil {
		logx.LogGateway("[ClaudGravityNext] Warning: MitmCA init failed, CONNECT-tunnel/TLS listeners skipped: %v", err)
		return nil, nil
	}
	logx.LogGateway("[ClaudGravityNext] Native MITM CA ready: %s", mitmCA.GetCAPath())
	mitm.GlobalMitmCA = mitmCA

	result := &mitmListeners{}
	proxySecret := cfgMgr.ProxySecret()

	connectSrv := &http.Server{
		Addr: fmt.Sprintf(":%d", connectPort),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.TrimSpace(proxySecret) == "" || !mitm.ProxyAuthOK(r, proxySecret) {
				w.Header().Set("Proxy-Authenticate", `Basic realm="SuperRelay Proxy"`)
				http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
				return
			}
			if r.Method == http.MethodConnect {
				mitm.HandleConnectTunnel(w, r, mitmCA, apiHandler, proxySecret, serverCfg.TLSPort)
				return
			}
			apiHandler.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		logx.LogGateway("[ClaudGravityNext] Listening on CONNECT-tunnel proxy :%d (HTTPS_PROXY target)", connectPort)
		if err := connectSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logx.LogGateway("[ClaudGravityNext] CONNECT-tunnel server error: %v", err)
		}
	}()
	result.connect = connectSrv

	gwCert, err := mitmCA.GetCertificate(serverCfg.PublicHost)
	if err != nil {
		logx.LogGateway("[ClaudGravityNext] Warning: could not mint gateway TLS cert, TLS API listener skipped: %v", err)
		return result, mitmCA
	}
	tlsSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", serverCfg.TLSPort),
		Handler:           apiHandler,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{gwCert}},
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		logx.LogGateway("[ClaudGravityNext] Listening on https://localhost:%d (TLS API, CA-signed, use as ANTHROPIC_BASE_URL)", serverCfg.TLSPort)
		if err := tlsSrv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			logx.LogGateway("[ClaudGravityNext] TLS API server error: %v", err)
		}
	}()
	result.tls = tlsSrv

	return result, mitmCA
}
