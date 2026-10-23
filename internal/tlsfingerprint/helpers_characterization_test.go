// Phase 1 characterization test for the ported tlsfingerprint package —
// proves the small pure helpers (canonicalAddr, fingerprintConnClosingBody)
// still behave as in v1.
package tlsfingerprint

import (
	"errors"
	"net"
	"net/url"
	"testing"
)

func TestCanonicalAddr_DefaultsPort443(t *testing.T) {
	u, _ := url.Parse("https://example.com/path")
	if got := canonicalAddr(u); got != "example.com:443" {
		t.Fatalf("expected default port 443, got %q", got)
	}
}

func TestCanonicalAddr_PreservesExplicitPort(t *testing.T) {
	u, _ := url.Parse("https://example.com:8443/path")
	if got := canonicalAddr(u); got != "example.com:8443" {
		t.Fatalf("expected explicit port to be preserved, got %q", got)
	}
}

type fakeReadCloser struct{ closeErr error }

func (f *fakeReadCloser) Read(p []byte) (int, error) { return 0, nil }
func (f *fakeReadCloser) Close() error               { return f.closeErr }

func TestFingerprintConnClosingBody_ClosesBothBodyAndConn(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	b := &fingerprintConnClosingBody{
		ReadCloser: &fakeReadCloser{closeErr: nil},
		conn:       clientConn,
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close must succeed when both body and conn close cleanly, got: %v", err)
	}
	// clientConn is now closed; writing to it must fail, proving Close()
	// genuinely closed the connection and not just the body.
	if _, err := clientConn.Write([]byte("x")); err == nil {
		t.Fatal("expected the underlying conn to be closed by fingerprintConnClosingBody.Close()")
	}
}

func TestFingerprintConnClosingBody_BodyErrorTakesPrecedence(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	bodyErr := errors.New("body close failed")
	b := &fingerprintConnClosingBody{
		ReadCloser: &fakeReadCloser{closeErr: bodyErr},
		conn:       clientConn,
	}
	if err := b.Close(); err != bodyErr {
		t.Fatalf("when the body close fails, that error must be returned (not the conn's), got: %v", err)
	}
}
