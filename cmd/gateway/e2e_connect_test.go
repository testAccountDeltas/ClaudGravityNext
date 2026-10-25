package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"claudgravity/internal/config"
)

const e2eTenantKey = "sk-e2e-tenant"

// e2eGateway starts the real CONNECT proxy + TLS listener around the real
// top-level handler (clistubs, /v1 router, dashboard) and returns what a
// client needs to behave like Claude Code behind HTTPS_PROXY.
type e2eGateway struct {
	connectPort int
	tlsPort     int
	secret      string
	caPEM       []byte
	cm          *config.ConfigManager
}

func startE2EGateway(t *testing.T) *e2eGateway {
	t.Helper()
	dir := t.TempDir()
	tlsPort, connectPort := freePort(t), freePort(t)
	cfgPath := filepath.Join(dir, "config.json")
	body, _ := json.Marshal(map[string]interface{}{
		"server":  map[string]interface{}{"port": freePort(t), "tls_port": tlsPort, "public_host": "gw.example.test"},
		"tenants": map[string]interface{}{e2eTenantKey: map[string]interface{}{"name": "e2e", "google_auth_type": "oauth_pool"}},
	})
	if err := os.WriteFile(cfgPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	cm, err := config.LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	caDir := filepath.Join(dir, "ca")
	srvs, ca := startMitmListeners(cm, caDir, connectPort, testTopHandler(cm))
	if srvs == nil || ca == nil {
		t.Fatal("listeners did not start")
	}
	t.Cleanup(func() { _ = srvs.connect.Close(); _ = srvs.tls.Close() })
	pem, err := os.ReadFile(filepath.Join(caDir, "superrelay-ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	g := &e2eGateway{connectPort: connectPort, tlsPort: tlsPort, secret: cm.ProxySecret(), caPEM: pem, cm: cm}
	g.waitUp(t)
	return g
}

func (g *e2eGateway) waitUp(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(g.connectPort)); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("CONNECT listener never came up")
}

// client behaves like a Node/Go HTTPS client with HTTPS_PROXY set to the
// gateway and the gateway's CA trusted (NODE_EXTRA_CA_CERTS).
func (g *e2eGateway) client(t *testing.T, secret string, trustCA bool) *http.Client {
	t.Helper()
	proxyURL, _ := url.Parse("http://127.0.0.1:" + strconv.Itoa(g.connectPort))
	tr := &http.Transport{
		Proxy:              http.ProxyURL(proxyURL),
		ProxyConnectHeader: http.Header{"Proxy-Authorization": []string{"Bearer " + secret}},
		DisableKeepAlives:  false,
		// Offer h2 like a modern client does; the interceptor must still pick http/1.1.
		ForceAttemptHTTP2: true,
	}
	if trustCA {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(g.caPEM) {
			t.Fatal("CA PEM not parseable")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, NextProtos: []string{"h2", "http/1.1"}}
	}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

func get(t *testing.T, c *http.Client, rawURL, key string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	if key != "" {
		req.Header.Set("x-api-key", key)
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestE2E_ClaudeThroughTheConnectProxy(t *testing.T) {
	g := startE2EGateway(t)
	c := g.client(t, g.secret, true)

	// The real hostname, the real route table, the gateway's own tenant list.
	resp, body := get(t, c, "https://api.anthropic.com/v1/models", e2eTenantKey)
	if resp.StatusCode != 200 || !strings.Contains(body, `"data"`) {
		t.Fatalf("GET /v1/models: %d %.200s", resp.StatusCode, body)
	}
	if resp.TLS == nil || resp.TLS.NegotiatedProtocol != "http/1.1" || resp.ProtoMajor != 1 {
		t.Fatalf("the intercepted connection must speak HTTP/1.1 even if the client offers h2: ALPN %q proto %s", resp.TLS.NegotiatedProtocol, resp.Proto)
	}
	cert := resp.TLS.PeerCertificates[0]
	if !contains(cert.DNSNames, "api.anthropic.com") {
		t.Fatalf("leaf SAN = %v, want api.anthropic.com", cert.DNSNames)
	}
	if time.Until(cert.NotAfter) < 24*time.Hour {
		t.Fatalf("the minted leaf expires too soon: %v", cert.NotAfter)
	}

	// A wrong key is an Anthropic-shaped 401, through the same tunnel.
	resp, body = get(t, c, "https://api.anthropic.com/v1/models", "sk-wrong")
	if resp.StatusCode != 401 || !strings.Contains(body, `"authentication_error"`) {
		t.Fatalf("bad key: %d %.200s", resp.StatusCode, body)
	}

	// Several requests keep working on the kept-alive tunnel, including a POST
	// that the gateway answers locally (no Google call involved).
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages/count_tokens",
			strings.NewReader(`{"model":"gemini-3.8-flash","messages":[{"role":"user","content":"count me"}]}`))
		req.Header.Set("x-api-key", e2eTenantKey)
		req.Header.Set("content-type", "application/json")
		r, err := c.Do(req)
		if err != nil {
			t.Fatalf("POST #%d: %v", i, err)
		}
		b, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if r.StatusCode != 200 || !strings.Contains(string(b), "input_tokens") {
			t.Fatalf("count_tokens #%d: %d %s", i, r.StatusCode, b)
		}
	}
}

func TestE2E_ClaudeAiHostIsInterceptedToo(t *testing.T) {
	g := startE2EGateway(t)
	resp, body := get(t, g.client(t, g.secret, true), "https://claude.ai/v1/models", e2eTenantKey)
	if resp.StatusCode != 200 || !strings.Contains(body, `"data"`) {
		t.Fatalf("%d %.200s", resp.StatusCode, body)
	}
	if !contains(resp.TLS.PeerCertificates[0].DNSNames, "claude.ai") {
		t.Fatalf("leaf SAN = %v", resp.TLS.PeerCertificates[0].DNSNames)
	}
}

func TestE2E_WrongOrMissingProxySecretNeverOpensATunnel(t *testing.T) {
	g := startE2EGateway(t)
	for name, secret := range map[string]string{"wrong": "not-the-secret", "empty": ""} {
		_, err := g.client(t, secret, true).Get("https://api.anthropic.com/v1/models")
		if err == nil || !strings.Contains(err.Error(), "407") && !strings.Contains(err.Error(), "Proxy Authentication Required") {
			t.Errorf("%s secret: want a 407 refusal, got %v", name, err)
		}
	}
}

func TestE2E_ClientThatDoesNotTrustTheCARejectsTheTunnel(t *testing.T) {
	g := startE2EGateway(t)
	_, err := g.client(t, g.secret, false).Get("https://api.anthropic.com/v1/models")
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("an untrusting client must fail the TLS handshake, got %v", err)
	}
}

func TestE2E_TunnelsToInternalTargetsAreRefused(t *testing.T) {
	g := startE2EGateway(t)
	c := g.client(t, g.secret, true)
	for _, target := range []string{"https://127.0.0.1/", "https://169.254.169.254/", "https://10.1.2.3/", "https://[::1]/", "https://[64:ff9b::7f00:1]/"} {
		_, err := c.Get(target)
		if err == nil || !strings.Contains(err.Error(), "Forbidden") && !strings.Contains(err.Error(), "403") {
			t.Errorf("%s: want the CONNECT refused with 403, got %v", target, err)
		}
	}
}

// A client may send its TLS ClientHello in the same TCP segment as the
// CONNECT request; the bytes then sit in net/http's read buffer and must not
// be lost when the connection is hijacked.
func TestE2E_ClientHelloPipelinedBehindConnect(t *testing.T) {
	g := startE2EGateway(t)
	raw, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(g.connectPort))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))

	connectReq := "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\nProxy-Authorization: Bearer " + g.secret + "\r\n\r\n"
	pc := &pipelinedConn{Conn: raw, prefix: []byte(connectReq), br: bufio.NewReader(raw)}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(g.caPEM)
	tc := tls.Client(pc, &tls.Config{RootCAs: pool, ServerName: "api.anthropic.com"})
	if err := tc.Handshake(); err != nil {
		t.Fatalf("handshake with a pipelined ClientHello: %v", err)
	}
	if !pc.sawEstablished {
		t.Fatal("the 200 Connection Established line was never seen")
	}
	_, _ = tc.Write([]byte("GET /v1/models HTTP/1.1\r\nHost: api.anthropic.com\r\nx-api-key: " + e2eTenantKey + "\r\nanthropic-version: 2023-06-01\r\nConnection: close\r\n\r\n"))
	resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"data"`) {
		t.Fatalf("%d %.200s", resp.StatusCode, b)
	}
}

// pipelinedConn sends the CONNECT request together with the first TLS write
// (one TCP write) and hides the proxy's "200 Connection Established" reply
// from the TLS layer.
type pipelinedConn struct {
	net.Conn
	prefix         []byte
	br             *bufio.Reader
	skipped        bool
	sawEstablished bool
}

func (p *pipelinedConn) Write(b []byte) (int, error) {
	if p.prefix != nil {
		out := append(append([]byte(nil), p.prefix...), b...)
		p.prefix = nil
		if _, err := p.Conn.Write(out); err != nil {
			return 0, err
		}
		return len(b), nil
	}
	return p.Conn.Write(b)
}

func (p *pipelinedConn) Read(b []byte) (int, error) {
	if !p.skipped {
		p.skipped = true
		line, err := p.br.ReadString('\n')
		if err != nil {
			return 0, err
		}
		p.sawEstablished = strings.Contains(line, "200")
		if _, err := p.br.ReadString('\n'); err != nil { // the blank line
			return 0, err
		}
	}
	return p.br.Read(b)
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// ANTHROPIC_BASE_URL=https://<host>:<tls_port> with the gateway CA trusted:
// the leaf must chain to the CA and name the configured public host.
func TestE2E_TLSListenerChainsToTheGatewayCA(t *testing.T) {
	g := startE2EGateway(t)
	host := g.cm.GetServerConfig().PublicHost
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(g.caPEM)
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: host},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(g.tlsPort))
		},
	}}
	defer c.CloseIdleConnections()
	var resp *http.Response
	var err error
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		req, _ := http.NewRequest(http.MethodGet, "https://"+host+"/v1/models", nil)
		req.Header.Set("x-api-key", e2eTenantKey)
		if resp, err = c.Do(req); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatalf("verified TLS request failed: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"data"`) {
		t.Fatalf("%d %.200s", resp.StatusCode, b)
	}
}
