package tlsfingerprint

import (
	"bufio"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// tlsServer starts an httptest TLS server (HTTP/2 when h2 is true) and a
// round tripper that trusts its certificate. Connections are counted.
type connStats struct{ opened, closed atomic.Int32 }

func (c *connStats) Load() int32 { return c.opened.Load() }

func tlsServer(t *testing.T, h2 bool, handler http.HandlerFunc) (*httptest.Server, *fingerprintedRoundTripper, *connStats) {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = h2
	conns := &connStats{}
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		switch s {
		case http.StateNew:
			conns.opened.Add(1)
		case http.StateClosed:
			conns.closed.Add(1)
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	rt := NewFingerprintedRoundTripper()
	rt.rootCAs = pool
	t.Cleanup(rt.CloseIdleConnections)
	return srv, rt, conns
}

func get(t *testing.T, rt http.RoundTripper, rawURL string) (*http.Response, string, error) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return nil, "", err
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, string(b), nil
}

func TestRoundTrip_HTTP2IsPooledAndReused(t *testing.T) {
	srv, rt, conns := tlsServer(t, true, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Proto) })
	for i := 0; i < 3; i++ {
		resp, body, err := get(t, rt, srv.URL+"/x")
		if err != nil || resp.StatusCode != 200 || body != "HTTP/2.0" {
			t.Fatalf("request %d: %v %v %q", i, resp, err, body)
		}
	}
	if conns.Load() != 1 {
		t.Fatalf("three requests must share one connection, saw %d", conns.Load())
	}
	rt.CloseIdleConnections()
	rt.mu.Lock()
	n := len(rt.h2Conns)
	rt.mu.Unlock()
	if n != 0 {
		t.Fatalf("CloseIdleConnections left %d pooled connections", n)
	}
	// After closing, the next request simply dials again.
	if _, body, err := get(t, rt, srv.URL); err != nil || body != "HTTP/2.0" {
		t.Fatalf("redial: %q %v", body, err)
	}
	if conns.Load() != 2 {
		t.Fatalf("expected a second connection after the pool was cleared, saw %d", conns.Load())
	}
}

func TestRoundTrip_FallsBackToHTTP1WhenALPNDoesNotSelectH2(t *testing.T) {
	srv, rt, conns := tlsServer(t, false, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, r.Proto) })
	for i := 0; i < 2; i++ {
		if _, body, err := get(t, rt, srv.URL); err != nil || body != "HTTP/1.1" {
			t.Fatalf("request %d: %q %v", i, body, err)
		}
	}
	// The HTTP/1.1 path has no keep-alive pool: nothing is reused (each
	// request even pays one extra probe connection for the ALPN check).
	if conns.Load() < 4 {
		t.Fatalf("no connection may be reused on the HTTP/1.1 path, saw %d for 2 requests", conns.Load())
	}
}

func TestRoundTrip_HTTP1BodyClosingReleasesTheConnection(t *testing.T) {
	srv, rt, conns := tlsServer(t, false, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "payload") })
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(resp.Body); string(b) != "payload" {
		t.Fatalf("body = %q", b)
	}
	_ = resp.Body.Close()
	for end := time.Now().Add(5 * time.Second); conns.closed.Load() < conns.opened.Load(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("closing the body must close the connection: opened=%d closed=%d", conns.opened.Load(), conns.closed.Load())
		}
	}
}

func TestRoundTrip_RejectsWhatItCannotServe(t *testing.T) {
	rt := NewFingerprintedRoundTripper()
	if _, err := rt.RoundTrip(&http.Request{}); err == nil || !strings.Contains(err.Error(), "no URL") {
		t.Fatalf("nil URL: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/", nil)
	if _, err := rt.RoundTrip(req); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("plain http must be refused (it would leak the fingerprint-free request): %v", err)
	}
}

func TestRoundTrip_UntrustedCertificateFails(t *testing.T) {
	srv, _, _ := tlsServer(t, true, func(http.ResponseWriter, *http.Request) {})
	rt := NewFingerprintedRoundTripper() // system roots only: the test CA is unknown
	if _, _, err := get(t, rt, srv.URL); err == nil {
		t.Fatal("a certificate from an unknown CA must not be accepted")
	}
}

func TestRoundTrip_DeadPeerDropsThePooledConnection(t *testing.T) {
	srv, rt, _ := tlsServer(t, true, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	if _, _, err := get(t, rt, srv.URL); err != nil {
		t.Fatal(err)
	}
	srv.CloseClientConnections()
	time.Sleep(100 * time.Millisecond)
	// The cached connection is dead: the request either redials or errors,
	// but it must never leave the dead connection in the pool.
	_, _, _ = get(t, rt, srv.URL)
	rt.mu.Lock()
	defer rt.mu.Unlock()
	for addr, c := range rt.h2Conns {
		if !c.CanTakeNewRequest() {
			t.Fatalf("dead connection to %s still pooled", addr)
		}
	}
}

func TestDial_ConnectionRefused(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()
	rt := NewFingerprintedRoundTripper()
	if _, _, err := get(t, rt, "https://"+addr+"/"); err == nil {
		t.Fatal("want a dial error")
	}
	// A failed dial must not wedge later requests to the same address.
	if _, _, err := get(t, rt, "https://"+addr+"/"); err == nil {
		t.Fatal("want a dial error again, not a hang or success")
	}
}

// connectProxy is a minimal HTTP proxy that answers CONNECT by tunnelling
// to the real test server regardless of the requested host.
type connectProxy struct {
	ln       net.Listener
	target   string
	status   int // reply status; 0 = 200
	requests chan *http.Request
}

func newConnectProxy(t *testing.T, target string, status int) *connectProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &connectProxy{ln: ln, target: target, status: status, requests: make(chan *http.Request, 8)}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.serve(c)
		}
	}()
	return p
}

func (p *connectProxy) serve(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	p.requests <- req
	if p.status != 0 && p.status != 200 {
		_, _ = io.WriteString(c, fmt.Sprintf("HTTP/1.1 %d %s\r\n\r\n", p.status, http.StatusText(p.status)))
		return
	}
	up, err := net.Dial("tcp", p.target)
	if err != nil {
		return
	}
	defer up.Close()
	_, _ = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, up); done <- struct{}{} }()
	<-done
}

func useProxy(t *testing.T, proxyURL string) {
	t.Helper()
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "HTTP_PROXY", "http_proxy"} {
		t.Setenv(k, "")
	}
	t.Setenv("HTTPS_PROXY", proxyURL)
}

// httptest's certificate is valid for example.com, which (unlike loopback
// names) the proxy settings do not bypass.
func proxiedURL(srv *httptest.Server) string {
	u, _ := url.Parse(srv.URL)
	return "https://example.com:" + u.Port() + "/proxied"
}

func TestDial_ThroughAnHTTPProxyWithCredentials(t *testing.T) {
	srv, rt, _ := tlsServer(t, true, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "via proxy "+r.URL.Path) })
	p := newConnectProxy(t, srv.Listener.Addr().String(), 0)
	useProxy(t, "http://user:p%40ss@"+p.ln.Addr().String())

	resp, body, err := get(t, rt, proxiedURL(srv))
	if err != nil || resp.StatusCode != 200 || body != "via proxy /proxied" {
		t.Fatalf("%v %q %v", resp, body, err)
	}
	req := <-p.requests
	u, _ := url.Parse(srv.URL)
	if req.Method != http.MethodConnect || req.Host != "example.com:"+u.Port() {
		t.Fatalf("CONNECT %q host %q", req.Method, req.Host)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:p@ss"))
	if got := req.Header.Get("Proxy-Authorization"); got != want {
		t.Fatalf("Proxy-Authorization = %q, want %q", got, want)
	}
}

func TestDial_ProxyWithoutCredentialsSendsNoAuthHeader(t *testing.T) {
	srv, rt, _ := tlsServer(t, true, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	p := newConnectProxy(t, srv.Listener.Addr().String(), 0)
	useProxy(t, "http://"+p.ln.Addr().String())
	if _, _, err := get(t, rt, proxiedURL(srv)); err != nil {
		t.Fatal(err)
	}
	if h := (<-p.requests).Header.Get("Proxy-Authorization"); h != "" {
		t.Fatalf("unexpected credentials: %q", h)
	}
}

func TestDial_ProxyRefusalAndUnreachableProxy(t *testing.T) {
	srv, rt, _ := tlsServer(t, true, func(http.ResponseWriter, *http.Request) {})
	p := newConnectProxy(t, srv.Listener.Addr().String(), http.StatusForbidden)
	useProxy(t, "http://"+p.ln.Addr().String())
	if _, _, err := get(t, rt, proxiedURL(srv)); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("a refused CONNECT must be reported: %v", err)
	}

	l, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := l.Addr().String()
	_ = l.Close()
	useProxy(t, "http://"+dead)
	if _, _, err := get(t, rt, proxiedURL(srv)); err == nil || !strings.Contains(err.Error(), "dial proxy") {
		t.Fatalf("unreachable proxy: %v", err)
	}
}

func TestDial_ProxyThatHangsUpMidHandshake(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close() // never answers the CONNECT
		}
	}()
	srv, rt, _ := tlsServer(t, true, func(http.ResponseWriter, *http.Request) {})
	useProxy(t, "http://"+ln.Addr().String())
	if _, _, err := get(t, rt, proxiedURL(srv)); err == nil {
		t.Fatal("want an error")
	}
}
