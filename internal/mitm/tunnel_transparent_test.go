package mitm

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

const tunnelSecret = "tunnel-test-secret"

// echoTarget accepts connections and echoes every byte back. closed receives
// a value whenever one of its connections ends (client hung up).
func echoTarget(t *testing.T) (port int, closed chan struct{}) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	closed = make(chan struct{}, 8)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
				closed <- struct{}{}
			}()
		}
	}()
	return l.Addr().(*net.TCPAddr).Port, closed
}

// proxyFor serves HandleConnectTunnel on a real listener, with allowedPort as
// the gateway's own TLS port (the only non-standard port a CONNECT may use).
func proxyFor(t *testing.T, allowedPort int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.NotFound(w, r)
			return
		}
		HandleConnectTunnel(w, r, nil, nil, tunnelSecret, allowedPort)
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

// resolvesTo makes checkConnectTarget answer with ips for any host, the way a
// public name resolving to a reachable address would.
func resolvesTo(t *testing.T, ips ...string) {
	t.Helper()
	orig := checkConnectTarget
	checkConnectTarget = func(string) ([]net.IP, error) {
		var out []net.IP
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
	t.Cleanup(func() { checkConnectTarget = orig })
}

func openTunnel(t *testing.T, proxyAddr, target string) (net.Conn, *bufio.Reader, string) {
	t.Helper()
	c, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	_, _ = c.Write([]byte("CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\nProxy-Authorization: Bearer " + tunnelSecret + "\r\n\r\n"))
	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("no CONNECT response: %v", err)
	}
	if strings.Contains(status, "200") {
		for {
			line, err := br.ReadString('\n')
			if err != nil || line == "\r\n" {
				break
			}
		}
	}
	return c, br, strings.TrimSpace(status)
}

func TestTunnel_TransparentRelayBothWays(t *testing.T) {
	port, _ := echoTarget(t)
	resolvesTo(t, "127.0.0.1")
	// "example.test" does not resolve anywhere: the tunnel must dial the
	// address the SSRF check approved, never look the name up again.
	c, br, status := openTunnel(t, proxyFor(t, port), "example.test:"+strconv.Itoa(port))
	if !strings.Contains(status, "200") {
		t.Fatalf("status = %q", status)
	}
	payload := make([]byte, 1<<20)
	_, _ = rand.Read(payload)
	go func() { _, _ = c.Write(payload) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(br, got); err != nil {
		t.Fatalf("echo read: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("1 MiB came back altered")
	}
}

func TestTunnel_ClientHangUpClosesTheTarget(t *testing.T) {
	port, closed := echoTarget(t)
	resolvesTo(t, "127.0.0.1")
	c, _, status := openTunnel(t, proxyFor(t, port), "example.test:"+strconv.Itoa(port))
	if !strings.Contains(status, "200") {
		t.Fatalf("status = %q", status)
	}
	_ = c.Close()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("the target connection stayed open after the client left (leaked tunnel)")
	}
}

func TestTunnel_TargetHangUpEndsTheClientSide(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		c, err := l.Accept()
		if err == nil {
			_, _ = c.Write([]byte("bye"))
			_ = c.Close()
		}
	}()
	port := l.Addr().(*net.TCPAddr).Port
	resolvesTo(t, "127.0.0.1")
	_, br, status := openTunnel(t, proxyFor(t, port), "example.test:"+strconv.Itoa(port))
	if !strings.Contains(status, "200") {
		t.Fatalf("status = %q", status)
	}
	all, err := io.ReadAll(br)
	if err != nil || string(all) != "bye" {
		t.Fatalf("client must see the data then a clean end: %q %v", all, err)
	}
}

func TestTunnel_FallsThroughToTheNextVerifiedAddress(t *testing.T) {
	port, _ := echoTarget(t)          // listens on IPv4 only
	resolvesTo(t, "::1", "127.0.0.1") // the first candidate refuses (or is unroutable)
	c, br, status := openTunnel(t, proxyFor(t, port), "example.test:"+strconv.Itoa(port))
	if !strings.Contains(status, "200") {
		t.Fatalf("status = %q, want the second address to be used", status)
	}
	_, _ = c.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo: %q %v", buf, err)
	}
}

func TestTunnel_UnreachableTargetIs502(t *testing.T) {
	l, _ := net.Listen("tcp4", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close() // nothing listens there any more
	resolvesTo(t, "127.0.0.1")
	_, _, status := openTunnel(t, proxyFor(t, port), "example.test:"+strconv.Itoa(port))
	if !strings.Contains(status, "502") {
		t.Fatalf("status = %q, want 502 Bad Gateway", status)
	}
}

func TestTunnel_RefusedTargetIsNeverDialled(t *testing.T) {
	port, closed := echoTarget(t)
	orig := checkConnectTarget
	checkConnectTarget = func(string) ([]net.IP, error) { return nil, io.ErrUnexpectedEOF }
	t.Cleanup(func() { checkConnectTarget = orig })
	_, _, status := openTunnel(t, proxyFor(t, port), "example.test:"+strconv.Itoa(port))
	if !strings.Contains(status, "403") {
		t.Fatalf("status = %q, want 403", status)
	}
	select {
	case <-closed:
		t.Fatal("the target was contacted although the SSRF check refused it")
	case <-time.After(300 * time.Millisecond):
	}
}

// Anthropic hosts are terminated locally, so they must never be resolved or
// checked as outbound targets (no DNS lookup, no dependence on the network).
func TestTunnel_AnthropicHostsSkipTheOutboundTargetCheck(t *testing.T) {
	orig := checkConnectTarget
	checkConnectTarget = func(host string) ([]net.IP, error) {
		t.Errorf("checkConnectTarget(%q) called for an intercepted host", host)
		return nil, io.ErrUnexpectedEOF
	}
	t.Cleanup(func() { checkConnectTarget = orig })

	ca, err := InitMitmCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		HandleConnectTunnel(w, r, ca, http.NotFoundHandler(), tunnelSecret, 8900)
	}))
	defer srv.Close()
	for _, host := range []string{"api.anthropic.com:443", "claude.ai:443", "statsig.anthropic.com:443"} {
		c, _, status := openTunnel(t, srv.Listener.Addr().String(), host)
		if !strings.Contains(status, "200") {
			t.Errorf("%s: status = %q", host, status)
		}
		_ = c.Close()
	}
}
