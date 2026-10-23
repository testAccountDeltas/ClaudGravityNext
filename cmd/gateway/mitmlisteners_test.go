package main

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"claudgravity/internal/clistubs"
	"claudgravity/internal/config"
	"claudgravity/internal/dashboard"
	"claudgravity/internal/httpapi"
	"claudgravity/internal/mitm"
)

// freePort asks the OS for an unused TCP port, then releases it
// immediately — same "find a free port, then race to bind it" pattern
// Go's own net/http tests use; acceptable flakiness for a local test.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func testConfigManager(t *testing.T, tlsPort int) *config.ConfigManager {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	body, _ := json.Marshal(map[string]interface{}{
		"server": map[string]interface{}{
			"port":     freePort(t),
			"tls_port": tlsPort,
		},
	})
	if err := os.WriteFile(cfgPath, body, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cm, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cm
}

// dummyAPIHandler stands in for httpapi.NewRouter(cfgMgr) so this test
// exercises only cmd/gateway's own wiring (CA init, CONNECT branch,
// TLS cert from the CA), not internal/httpapi's routing — already covered
// by internal/httpapi's own tests.
func dummyAPIHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Dummy-Api", "hit")
		w.WriteHeader(http.StatusTeapot)
	})
}

// TestStartMitmListeners_CAGeneratedUnderOwnDir confirms InitMitmCA writes
// its files under the caller-supplied caDir (never superrelay's "certs"
// dir) and that GlobalMitmCA gets set, matching v1's package-level
// globalMitmCA assignment in server.go.
func TestStartMitmListeners_CAGeneratedUnderOwnDir(t *testing.T) {
	caDir := t.TempDir()
	tlsPort := freePort(t)
	connectPort := freePort(t)
	cm := testConfigManager(t, tlsPort)

	srvs, ca := startMitmListeners(cm, caDir, connectPort, dummyAPIHandler())
	if srvs == nil || ca == nil {
		t.Fatalf("startMitmListeners returned nil (CA init failed?)")
	}
	defer func() {
		_ = srvs.connect.Close()
		_ = srvs.tls.Close()
	}()

	if _, err := os.Stat(filepath.Join(caDir, "superrelay-ca.pem")); err != nil {
		t.Errorf("expected CA cert file under caDir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(caDir, "superrelay-ca.key")); err != nil {
		t.Errorf("expected CA key file under caDir: %v", err)
	}
}

// TestStartMitmListeners_TLSListenerServesRouter confirms the TLS listener
// actually answers HTTPS requests with a CA-minted leaf cert and reaches
// the supplied handler — the one piece of this wiring step that
// internal/mitm's own characterization tests (frozen, unit-level) cannot
// cover, since they never open a real socket.
func TestStartMitmListeners_TLSListenerServesRouter(t *testing.T) {
	caDir := t.TempDir()
	tlsPort := freePort(t)
	connectPort := freePort(t)
	cm := testConfigManager(t, tlsPort)

	srvs, _ := startMitmListeners(cm, caDir, connectPort, dummyAPIHandler())
	if srvs == nil {
		t.Fatalf("startMitmListeners returned nil")
	}
	defer func() {
		_ = srvs.connect.Close()
		_ = srvs.tls.Close()
	}()

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	var resp *http.Response
	var err error
	// The TLS listener's goroutine may not have called ListenAndServeTLS
	// yet; retry briefly instead of sleeping a fixed guess.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = client.Get("https://127.0.0.1:" + strconv.Itoa(tlsPort) + "/anything")
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET over TLS listener failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d (dummy handler reached)", resp.StatusCode, http.StatusTeapot)
	}
	if resp.Header.Get("X-Dummy-Api") != "hit" {
		t.Errorf("expected request to reach the supplied apiHandler, got headers %v", resp.Header)
	}
}

// TestStartMitmListeners_ConnectPortFallsThroughToHandler confirms a
// non-CONNECT request on the tunnel port requires valid proxy authentication,
// and when authenticated is routed to the apiHandler.
func TestStartMitmListeners_ConnectPortFallsThroughToHandler(t *testing.T) {
	caDir := t.TempDir()
	tlsPort := freePort(t)
	connectPort := freePort(t)
	cm := testConfigManager(t, tlsPort)

	srvs, _ := startMitmListeners(cm, caDir, connectPort, dummyAPIHandler())
	if srvs == nil {
		t.Fatalf("startMitmListeners returned nil")
	}
	defer func() {
		_ = srvs.connect.Close()
		_ = srvs.tls.Close()
	}()

	client := &http.Client{Timeout: 5 * time.Second}

	// 1. Unauthenticated request must be rejected with 407.
	var resp *http.Response
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = client.Get("http://127.0.0.1:" + strconv.Itoa(connectPort) + "/anything")
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET over CONNECT-tunnel port failed: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusProxyAuthRequired {
		t.Errorf("unauthenticated status = %d, want %d (proxy auth required)", resp.StatusCode, http.StatusProxyAuthRequired)
	}

	// 2. Authenticated request falls through to apiHandler.
	authedReq, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+strconv.Itoa(connectPort)+"/anything", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	authedReq.Header.Set("Proxy-Authorization", "Bearer "+cm.ProxySecret())
	authedResp, err := client.Do(authedReq)
	if err != nil {
		t.Fatalf("authenticated GET over CONNECT-tunnel port failed: %v", err)
	}
	defer authedResp.Body.Close()
	_, _ = io.ReadAll(authedResp.Body)
	if authedResp.StatusCode != http.StatusTeapot {
		t.Errorf("authenticated status = %d, want %d (dummy handler reached via non-CONNECT fallback)", authedResp.StatusCode, http.StatusTeapot)
	}
}

// TestStartMitmListeners_ConnectRejectsNonStandardPort exercises
// mitm.HandleConnectTunnel's own port-allowlist straight through this
// wiring, confirming localTLSPort (serverCfg.TLSPort) is threaded through
// correctly as the fourth allowed port, and that a CONNECT to some other
// arbitrary port is refused — the ConnectAllowlistSelfConsistency
// invariant (spec Part 7.6 rule 8) exercised live, not just in
// internal/mitm's own unit tests.
func TestStartMitmListeners_ConnectRejectsNonStandardPort(t *testing.T) {
	caDir := t.TempDir()
	tlsPort := freePort(t)
	connectPort := freePort(t)
	cm := testConfigManager(t, tlsPort)

	srvs, _ := startMitmListeners(cm, caDir, connectPort, dummyAPIHandler())
	if srvs == nil {
		t.Fatalf("startMitmListeners returned nil")
	}
	defer func() {
		_ = srvs.connect.Close()
		_ = srvs.tls.Close()
	}()

	var conn net.Conn
	var err error
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err = net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(connectPort))
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("dial connect-tunnel port: %v", err)
	}
	defer conn.Close()

	// example.org:9999 is neither 443/8443/80 nor the configured TLSPort —
	// HandleConnectTunnel must refuse it with 403 before ever hijacking.
	// A valid Proxy-Authorization is required so this exercises the port
	// check specifically, not the (also-real, separately testable) 407
	// auth-rejection path.
	req := "CONNECT example.org:9999 HTTP/1.1\r\n" +
		"Host: example.org:9999\r\n" +
		"Proxy-Authorization: Bearer " + cm.ProxySecret() + "\r\n\r\n"
	_, _ = conn.Write([]byte(req))
	buf := make([]byte, 512)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	line := string(buf[:n])
	if !hasStatus403(line) {
		t.Errorf("expected 403 Forbidden for non-standard CONNECT port, got: %q", line)
	}
}

func hasStatus403(statusLine string) bool {
	return len(statusLine) >= 15 && statusLine[9:12] == "403"
}

func testTopHandler(cm *config.ConfigManager) http.Handler {
	topMux := http.NewServeMux()
	clistubs.RegisterHandlers(topMux, httpapi.CommonAPIMiddleware)
	topMux.Handle("/v1/", httpapi.NewRouter(cm))
	dashboard.RegisterDashboardRoutes(topMux, cm)
	topMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		isAPI := mitm.IsAnthropicHost(r.Host) ||
			r.Header.Get("x-api-key") != "" ||
			strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-") ||
			r.Header.Get("anthropic-version") != ""
		if r.URL.Path != "/" || isAPI {
			mitm.PassthroughToReal(w, r)
			return
		}
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	})
	return httpapi.RecoverMiddleware(topMux)
}

// TestCORS_DashboardRoutesDoNotHaveWildcardCORS verifies that dashboard routes
// (/, /dashboard, /dashboard/, /api/keys, /api/accounts, /ws) do NOT have wildcard CORS headers,
// while API routes (/v1/...) and CLI stubs (/api/auth/current_user) DO.
func TestCORS_DashboardRoutesDoNotHaveWildcardCORS(t *testing.T) {
	cm := testConfigManager(t, 8900)
	apiHandler := testTopHandler(cm)

	dashboardPaths := []string{
		"/",
		"/dashboard",
		"/dashboard/",
		"/api/keys",
		"/api/accounts",
		"/api/logs",
		"/ws",
	}

	for _, p := range dashboardPaths {
		req := httptest.NewRequest(http.MethodOptions, p, nil)
		rec := httptest.NewRecorder()
		apiHandler.ServeHTTP(rec, req)
		if origin := rec.Header().Get("Access-Control-Allow-Origin"); origin == "*" {
			t.Errorf("path %s must NOT have wildcard CORS Access-Control-Allow-Origin: *", p)
		}
	}

	// CLI stub route must have CORS
	reqCli := httptest.NewRequest(http.MethodOptions, "/api/auth/current_user", nil)
	recCli := httptest.NewRecorder()
	apiHandler.ServeHTTP(recCli, reqCli)
	if origin := recCli.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Errorf("CLI stub must have Access-Control-Allow-Origin: *, got %q", origin)
	}

	// API route must have CORS
	reqAPI := httptest.NewRequest(http.MethodOptions, "/v1/models", nil)
	recAPI := httptest.NewRecorder()
	apiHandler.ServeHTTP(recAPI, reqAPI)
	if origin := recAPI.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Errorf("/v1/models must have Access-Control-Allow-Origin: *, got %q", origin)
	}
}

// TestTopMux_DashboardAndPassthroughRouting verifies:
// 1. "/" redirects to "/dashboard" for non-Anthropic hosts.
// 2. "/dashboard" and "/dashboard/" require Basic Auth.
// 3. Unmatched endpoints on topMux do not return 401 Basic Auth.
func TestTopMux_DashboardAndPassthroughRouting(t *testing.T) {
	cm := testConfigManager(t, 8900)
	apiHandler := testTopHandler(cm)

	// 1. Root redirect to /dashboard when not an API request
	reqRoot := httptest.NewRequest(http.MethodGet, "/", nil)
	recRoot := httptest.NewRecorder()
	apiHandler.ServeHTTP(recRoot, reqRoot)
	if recRoot.Code != http.StatusFound {
		t.Errorf("GET /: got status %d, want 302 Found", recRoot.Code)
	}
	if loc := recRoot.Header().Get("Location"); loc != "/dashboard" {
		t.Errorf("GET /: got Location %q, want /dashboard", loc)
	}

	// Root with API key or Bearer sk- falls through to PassthroughToReal (not redirect or 401)
	for _, tc := range []struct{ h, v string }{{"x-api-key", "sk-test"}, {"Authorization", "Bearer sk-test"}} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(tc.h, tc.v)
		rec := httptest.NewRecorder()
		apiHandler.ServeHTTP(rec, req)
		if rec.Code == http.StatusFound || rec.Code == http.StatusUnauthorized {
			t.Errorf("GET / with %s got %d, must not redirect or challenge", tc.h, rec.Code)
		}
	}

	// 2. /dashboard and /dashboard/ require Basic Auth
	for _, p := range []string{"/dashboard", "/dashboard/"} {
		reqDash := httptest.NewRequest(http.MethodGet, p, nil)
		recDash := httptest.NewRecorder()
		apiHandler.ServeHTTP(recDash, reqDash)
		if recDash.Code != http.StatusUnauthorized || !strings.Contains(recDash.Header().Get("WWW-Authenticate"), "Basic") {
			t.Errorf("GET %s without auth: got %d (want 401 Basic)", p, recDash.Code)
		}

		reqDashAuthed := httptest.NewRequest(http.MethodGet, p, nil)
		reqDashAuthed.SetBasicAuth("admin", cm.DashboardPassword())
		recDashAuthed := httptest.NewRecorder()
		apiHandler.ServeHTTP(recDashAuthed, reqDashAuthed)
		if recDashAuthed.Code != http.StatusOK {
			t.Errorf("GET %s with auth: got %d, want 200", p, recDashAuthed.Code)
		}
	}

	// 3. Unmatched endpoint must NOT return 401 Basic Auth
	reqUnknown := httptest.NewRequest(http.MethodGet, "/api/unknown_future_endpoint", nil)
	recUnknown := httptest.NewRecorder()
	apiHandler.ServeHTTP(recUnknown, reqUnknown)
	if recUnknown.Code == http.StatusUnauthorized {
		t.Errorf("unmatched endpoint returned 401 Basic Auth — should have passed through to PassthroughToReal")
	}

	// 4. Intercepted MITM handler also falls through to PassthroughToReal (never 401)
	interceptedMux := http.NewServeMux()
	clistubs.RegisterHandlers(interceptedMux, httpapi.CommonAPIMiddleware)
	interceptedMux.Handle("/v1/", httpapi.NewRouter(cm))
	interceptedMux.HandleFunc("/", mitm.PassthroughToReal)
	var mitmHandler http.Handler = httpapi.RecoverMiddleware(interceptedMux)

	recMitm := httptest.NewRecorder()
	mitmHandler.ServeHTTP(recMitm, reqUnknown)
	if recMitm.Code == http.StatusUnauthorized {
		t.Errorf("intercepted unmatched endpoint returned 401 Basic Auth")
	}
}
