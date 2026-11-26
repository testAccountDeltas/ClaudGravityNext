package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"claudgravity/internal/config"
	"golang.org/x/net/websocket"
)

func TestMountArtifacts_WiresViewerAndAuthBoundaries(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfgBody, _ := json.Marshal(map[string]any{
		"server":  map[string]any{"dashboard_password": "secret"},
		"tenants": map[string]any{"sk-art-test": map[string]any{"name": "t"}},
	})
	if err := os.WriteFile(cfgPath, cfgBody, 0o644); err != nil {
		t.Fatal(err)
	}
	cm, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	topMux := http.NewServeMux()
	interceptedMux := http.NewServeMux()
	store := mountArtifactsAt(cm, filepath.Join(dir, "artifacts.db"), topMux, interceptedMux)
	if store == nil {
		t.Fatal("expected artifacts store to open")
	}
	t.Cleanup(func() { _ = store.Close() })

	// Publishing via /api/frame/deploy/direct requires tenant key.
	rec := httptest.NewRecorder()
	interceptedMux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/frame/deploy/direct", strings.NewReader(`{"title":"T","content":"<b>ok</b>"}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed frame deploy = %d, want 401", rec.Code)
	}

	pubReq := httptest.NewRequest("POST", "/api/frame/deploy/direct", strings.NewReader(`{"title":"T","content":"<b>ok</b>"}`))
	pubReq.Header.Set("Authorization", "Bearer sk-art-test")
	rec = httptest.NewRecorder()
	interceptedMux.ServeHTTP(rec, pubReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed frame deploy = %d %s", rec.Code, rec.Body.String())
	}
	var pub map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &pub)
	slug := pub["slug"].(string)

	// Viewer is reachable on BOTH topMux and interceptedMux without tenant bearer key.
	for _, m := range []*http.ServeMux{topMux, interceptedMux} {
		rec = httptest.NewRecorder()
		m.ServeHTTP(rec, httptest.NewRequest("GET", "/code/artifact/"+slug, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "artifactFrame") {
			t.Fatalf("viewer on mux returned %d", rec.Code)
		}
	}

	// Boot returns a live subscriptionToken and /api/frame/watch-token/{slug}
	// renews it; /edge-api/frame-live/{slug}/ws rejects missing subprotocol tokens.
	bootReq := httptest.NewRequest("GET", "/api/frame/"+slug+"?via=model_read", nil)
	bootReq.Header.Set("Authorization", "Bearer sk-art-test")
	rec = httptest.NewRecorder()
	interceptedMux.ServeHTTP(rec, bootReq)
	var bootResp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &bootResp)
	if rec.Code != http.StatusOK || !strings.HasPrefix(bootResp["subscriptionToken"].(string), "fl1_") {
		t.Fatalf("intercepted boot missing live subscriptionToken: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	interceptedMux.ServeHTTP(rec, httptest.NewRequest("GET", "/edge-api/frame-live/"+slug+"/ws", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed frame-live ws = %d, want 401", rec.Code)
	}

	// Dashboard list API is on topMux behind dashboard Basic Auth (not on interceptedMux).
	rec = httptest.NewRecorder()
	topMux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/dashboard/artifacts", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed dashboard artifacts = %d, want 401", rec.Code)
	}
	dashReq := httptest.NewRequest("GET", "/api/dashboard/artifacts", nil)
	dashReq.SetBasicAuth("admin", "secret")
	rec = httptest.NewRecorder()
	topMux.ServeHTTP(rec, dashReq)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), slug) {
		t.Fatalf("authed dashboard artifacts = %d %s", rec.Code, rec.Body.String())
	}
}

func TestMountArtifacts_ConnectTunnelLiveWebSocket(t *testing.T) {
	dir := t.TempDir()
	tlsPort, connectPort := freePort(t), freePort(t)
	cfgPath := filepath.Join(dir, "config.json")
	cfgBody, _ := json.Marshal(map[string]any{
		"server":  map[string]any{"port": freePort(t), "tls_port": tlsPort, "public_host": "gw.example.test"},
		"tenants": map[string]any{"sk-art-e2e": map[string]any{"name": "e2e"}},
	})
	if err := os.WriteFile(cfgPath, cfgBody, 0o644); err != nil {
		t.Fatal(err)
	}
	cm, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}

	interceptedMux := http.NewServeMux()
	store := mountArtifactsAt(cm, filepath.Join(dir, "artifacts.db"), nil, interceptedMux)
	if store == nil {
		t.Fatal("expected artifacts store")
	}
	t.Cleanup(func() { _ = store.Close() })

	caDir := filepath.Join(dir, "ca")
	srvs, _ := startMitmListeners(cm, caDir, connectPort, interceptedMux)
	if srvs == nil {
		t.Fatal("mitm listeners did not start")
	}
	t.Cleanup(func() { _ = srvs.connect.Close(); _ = srvs.tls.Close() })

	caPEM, err := os.ReadFile(filepath.Join(caDir, "superrelay-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)

	// Publish & boot through interceptedMux to mint a live token.
	pubReq := httptest.NewRequest("POST", "/api/frame/deploy/direct", strings.NewReader(`{"title":"TunnelLive","content":"<h1>v1</h1>"}`))
	pubReq.Header.Set("Authorization", "Bearer sk-art-e2e")
	rec := httptest.NewRecorder()
	interceptedMux.ServeHTTP(rec, pubReq)
	var dep map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &dep)
	slug := dep["slug"].(string)

	bootReq := httptest.NewRequest("GET", "/api/frame/"+slug+"?via=model_read", nil)
	bootReq.Header.Set("Authorization", "Bearer sk-art-e2e")
	rec = httptest.NewRecorder()
	interceptedMux.ServeHTTP(rec, bootReq)
	var boot map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &boot)
	tok := boot["subscriptionToken"].(string)

	// Connect over the real CONNECT tunnel to wss://claude.ai/edge-api/frame-live/{slug}/ws.
	var raw net.Conn
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(25 * time.Millisecond) {
		raw, err = net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(connectPort))
		if err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("dial CONNECT port: %v", err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))

	authB64 := base64.StdEncoding.EncodeToString([]byte(cm.ProxySecret() + ":x"))
	_, _ = raw.Write([]byte("CONNECT claude.ai:443 HTTP/1.1\r\nHost: claude.ai:443\r\nProxy-Authorization: Basic " + authB64 + "\r\n\r\n"))
	br := bufio.NewReader(raw)
	statusLine, err := br.ReadString('\n')
	if err != nil || !strings.Contains(statusLine, "200") {
		t.Fatalf("CONNECT reply = %q, err=%v", statusLine, err)
	}
	for {
		line, lerr := br.ReadString('\n')
		if lerr != nil || strings.TrimSpace(line) == "" {
			break
		}
	}

	tc := tls.Client(raw, &tls.Config{RootCAs: pool, ServerName: "claude.ai"})
	if err := tc.Handshake(); err != nil {
		t.Fatalf("TLS handshake for claude.ai: %v", err)
	}
	cfg, err := websocket.NewConfig("wss://claude.ai/edge-api/frame-live/"+slug+"/ws", "https://claude.ai")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Protocol = []string{"frame-live.v1", tok}
	ws, err := websocket.NewClient(cfg, tc)
	if err != nil {
		t.Fatalf("websocket upgrade through CONNECT MITM failed: %v", err)
	}
	defer ws.Close()

	if err := websocket.Message.Send(ws, "hb"); err != nil {
		t.Fatal(err)
	}
	var pong string
	if err := websocket.Message.Receive(ws, &pong); err != nil || pong != "pong" {
		t.Fatalf("expected pong, got %q (err=%v)", pong, err)
	}

	// Redeploy v2 and confirm the push reaches the CONNECT MITM WebSocket.
	upReq := httptest.NewRequest("POST", "/api/frame/deploy/direct", strings.NewReader(`{"slug":"`+slug+`","content":"<h1>v2</h1>"}`))
	upReq.Header.Set("Authorization", "Bearer sk-art-e2e")
	rec = httptest.NewRecorder()
	interceptedMux.ServeHTTP(rec, upReq)
	var verMsg string
	if err := websocket.Message.Receive(ws, &verMsg); err != nil || !strings.Contains(verMsg, `"ver":"2"`) {
		t.Fatalf("expected version push on WS, got %q (err=%v)", verMsg, err)
	}
}
