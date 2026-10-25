package mitm

import (
	"bytes"
	"crypto/x509"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCov_CAPersistsAndReloads(t *testing.T) {
	dir := t.TempDir()
	ca1, err := InitMitmCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ca1.GetCAPEM(), "BEGIN CERTIFICATE") {
		t.Fatal("PEM missing")
	}
	if p := ca1.GetCAPath(); !filepath.IsAbs(p) || filepath.Base(p) != "superrelay-ca.pem" {
		t.Fatalf("CA path: %q", p)
	}
	ca2, err := InitMitmCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ca1.GetCAPEM() != ca2.GetCAPEM() {
		t.Fatal("existing CA must be reused, not regenerated")
	}
	// Corrupt key => a fresh CA is generated and persisted.
	if err := os.WriteFile(filepath.Join(dir, "superrelay-ca.key"), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	ca3, err := InitMitmCA(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ca3.GetCAPEM() == ca1.GetCAPEM() {
		t.Fatal("corrupt key must trigger regeneration")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "superrelay-ca.pem")); string(b) != ca3.GetCAPEM() {
		t.Fatal("regenerated CA not persisted")
	}
	// Unusable directory is an error.
	f := filepath.Join(dir, "afile")
	_ = os.WriteFile(f, []byte("x"), 0o600)
	if _, err := InitMitmCA(filepath.Join(f, "sub")); err == nil {
		t.Fatal("expected MkdirAll error")
	}
}

func TestCov_GetCertificateChainsToCA(t *testing.T) {
	ca, err := InitMitmCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(ca.GetCAPEM())) {
		t.Fatal("CA PEM not parseable")
	}
	for _, host := range []string{"API.Anthropic.com", "10.1.2.3"} {
		c, err := ca.GetCertificate(host)
		if err != nil {
			t.Fatal(err)
		}
		leaf, err := x509.ParseCertificate(c.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		name := strings.ToLower(host)
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name}); err != nil {
			t.Fatalf("%s must verify against CA: %v", host, err)
		}
		again, _ := ca.GetCertificate(strings.ToLower(host))
		if !bytes.Equal(again.Certificate[0], c.Certificate[0]) {
			t.Fatalf("%s: second lookup must hit the (case-insensitive) cache", host)
		}
	}
	// Loopback is always a SAN.
	c, _ := ca.GetCertificate("example.test")
	leaf, _ := x509.ParseCertificate(c.Certificate[0])
	if err := leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Fatalf("loopback SAN: %v", err)
	}
}

func TestCov_BufferedConnReadsPrefixFirst(t *testing.T) {
	bc := &bufferedConn{r: io.MultiReader(strings.NewReader("abc"), strings.NewReader("def"))}
	b, _ := io.ReadAll(bc)
	if string(b) != "abcdef" {
		t.Fatalf("got %q", b)
	}
}

func TestCov_SetSecretMatcherNilResets(t *testing.T) {
	SetSecretMatcher(func(s string) bool { return s == "k" })
	if !isGatewaySecret(" k ") || isGatewaySecret("") || isGatewaySecret("other") {
		t.Fatal("matcher semantics (trimmed, empty never a secret)")
	}
	SetSecretMatcher(nil)
	if isGatewaySecret("k") {
		t.Fatal("nil matcher must match nothing")
	}
}

func TestCov_CloseTerminatesChunkedStream(t *testing.T) {
	conn := &fakeConn{}
	w := newMitmResponseWriter(conn)
	_, _ = w.Write([]byte("part1"))
	w.Flush()
	_, _ = w.Write([]byte("part2"))
	w.Close()
	out := conn.written.String()
	if !strings.Contains(out, "Transfer-Encoding: chunked") || strings.Contains(out, "Content-Length") {
		t.Fatalf("streamed response must be chunked: %q", out)
	}
	if !strings.HasSuffix(out, "0\r\n\r\n") || !strings.Contains(out, "part2") {
		t.Fatalf("trailing chunk + terminator missing: %q", out)
	}
	// Single-shot: Close alone sends an exact Content-Length, no terminator.
	c2 := &fakeConn{}
	w2 := newMitmResponseWriter(c2)
	_, _ = w2.Write([]byte("hello"))
	w2.Close()
	o2 := c2.written.String()
	if !strings.Contains(o2, "Content-Length: 5") || strings.Contains(o2, "0\r\n\r\n") || !strings.HasSuffix(o2, "hello") {
		t.Fatalf("single-shot: %q", o2)
	}
}
