package mitm

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "api.anthropic.com"},
		DNSNames: []string{"api.anthropic.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// tcpPair returns a connected loopback TCP pair. Real sockets rather than
// net.Pipe: the server's deferred tls Close writes a close_notify, which on
// the unbuffered pipe blocks until the peer reads.
func tcpPair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	acc := make(chan net.Conn, 1)
	go func() { c, _ := ln.Accept(); acc <- c }()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	server = <-acc
	t.Cleanup(func() { client.Close(); server.Close() })
	return client, server
}

// interceptPair returns the client side of a TLS connection whose server side
// is being driven by serveInterceptedTLS, plus a channel closed when that
// function returns.
func interceptPair(t *testing.T, h http.Handler) (*tls.Conn, <-chan struct{}) {
	t.Helper()
	cc, sc := tcpPair(t)
	srv := tls.Server(sc, &tls.Config{Certificates: []tls.Certificate{selfSignedCert(t)}, NextProtos: []string{"http/1.1"}})
	done := make(chan struct{})
	go func() { defer close(done); serveInterceptedTLS(srv, h) }()
	return tls.Client(cc, &tls.Config{InsecureSkipVerify: true, ServerName: "api.anthropic.com"}), done
}

func waitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("serveInterceptedTLS did not return: %s", what)
	}
}

func TestServeInterceptedTLS_LargeBodyRoundTrip(t *testing.T) {
	body := strings.Repeat("0123456789abcdef", 8192) // 128 KiB, several TLS records
	var got []byte
	cli, done := interceptPair(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("echo:" + r.URL.Path + ":" + r.Header.Get("X-Test")))
	}))
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", strings.NewReader(body))
	req.Header.Set("X-Test", "yes")
	if err := req.Write(cli); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(cli), req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(out) != "echo:/v1/messages:yes" {
		t.Fatalf("status %d body %q", resp.StatusCode, out)
	}
	if !bytes.Equal(got, []byte(body)) {
		t.Fatalf("handler saw %d body bytes, want %d (corrupted or truncated)", len(got), len(body))
	}
	cli.Close()
	waitDone(t, done, "after response")
}

func TestServeInterceptedTLS_ClientDisconnectCancelsContext(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	cli, done := interceptPair(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
		}
	}))
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/x", strings.NewReader(`{}`))
	if err := req.Write(cli); err != nil {
		t.Fatal(err)
	}
	<-started
	cli.Close() // Ctrl+C on the client
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("request context was not cancelled when the client hung up")
	}
	waitDone(t, done, "after disconnect")
}

func TestServeInterceptedTLS_HandlerPanicIsRecovered(t *testing.T) {
	cli, done := interceptPair(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	req, _ := http.NewRequest("GET", "https://api.anthropic.com/x", nil)
	if err := req.Write(cli); err != nil {
		t.Fatal(err)
	}
	waitDone(t, done, "after handler panic") // test process surviving is the assertion
}

func TestServeInterceptedTLS_NonTLSAndGarbageRequests(t *testing.T) {
	cc, sc := tcpPair(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		serveInterceptedTLS(tls.Server(sc, &tls.Config{Certificates: []tls.Certificate{selfSignedCert(t)}}), http.NotFoundHandler())
	}()
	_, _ = cc.Write([]byte("GET / HTTP/1.1\r\n\r\n")) // plaintext into a TLS endpoint
	cc.Close()
	waitDone(t, done, "failed handshake")

	cli, done2 := interceptPair(t, http.NotFoundHandler())
	if err := cli.Handshake(); err != nil {
		t.Fatal(err)
	}
	_, _ = cli.Write([]byte("NOT HTTP AT ALL\r\n\r\n"))
	waitDone(t, done2, "unparseable request")
}

func TestIdleCopy_ForwardsThenStopsOnIdle(t *testing.T) {
	srcW, srcR := tcpPair(t) // srcW -> srcR
	dstW, dstR := tcpPair(t) // dstW -> dstR
	done := make(chan struct{})
	go func() { defer close(done); idleCopy(dstW, srcR, 300*time.Millisecond) }()

	go func() {
		for i := 0; i < 3; i++ { // pauses shorter than the deadline must not end the copy
			_, _ = srcW.Write([]byte("chunk"))
			time.Sleep(150 * time.Millisecond)
		}
	}()
	buf := make([]byte, 15)
	_ = dstR.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(dstR, buf); err != nil || string(buf) != "chunkchunkchunk" {
		t.Fatalf("forwarded %q, err %v", buf, err)
	}
	select {
	case <-done: // genuine silence ends it
	case <-time.After(3 * time.Second):
		t.Fatal("idleCopy did not stop after the idle deadline")
	}
}

func TestIdleCopy_StopsWhenSourceCloses(t *testing.T) {
	srcW, srcR := tcpPair(t)
	dstW, _ := tcpPair(t)
	done := make(chan struct{})
	go func() { defer close(done); idleCopy(dstW, srcR, time.Minute) }()
	srcW.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("idleCopy kept running after EOF")
	}
}
