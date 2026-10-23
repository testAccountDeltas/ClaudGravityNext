// Package tlsfingerprint is a port of superrelay (v1)'s
// tlsfingerprint.go, split across four files by responsibility per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 4.3:
// roundtripper.go (this file — the type, constructor, RoundTrip entry
// point, connection-pool bookkeeping), h2.go (h2ConnFor/dialH2), dial.go
// (dialTLS), http1.go (roundTripHTTP1 fallback).
//
// Per Part 3 p.5 and Part 7.2 of the spec, this package is EXPLICITLY
// frozen — not just "ported carefully" but "not touched at all" beyond
// the mechanical package-name/import-path change. Every function body
// below is byte-for-byte unchanged from v1. This is JA3/JA4 TLS-fingerprint
// mimicry: Go's crypto/tls emits a ClientHello no shipping browser
// produces, so without this, outbound traffic to Google is trivially
// distinguishable BEFORE a single byte of the actual request is
// inspected. The ClientHello bytes this produces are randomized per
// connection BY DESIGN (uTLS's HelloChrome_Auto profile, matching real
// Chrome's own anti-fingerprinting randomization) — see
// TestFreeze_TLSFingerprintProfile in roundtripper_characterization_test.go
// for why the characterization test freezes the CONFIGURATION CHOICE
// (which ClientHelloID), not the wire bytes.
package tlsfingerprint

import (
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

const fingerprintDialTimeout = 30 * time.Second

// Health-check bounds for pooled HTTP/2 connections — matches the values
// clirelay settled on for this exact Google-facing traffic: ReadIdleTimeout
// detects a peer that silently stopped answering (NAT/load-balancer drop
// without FIN/RST), PingTimeout bounds the PONG wait before tearing the
// connection down, and IdleConnTimeout bounds how long an unused connection
// is kept around at all.
const (
	h2ReadIdleTimeout = 30 * time.Second
	h2PingTimeout     = 15 * time.Second
	h2IdleConnTimeout = 90 * time.Second
)

// fingerprintedRoundTripper performs HTTPS requests over connections whose
// ClientHello matches a real browser, managing its own HTTP/2 connections
// since Go only routes a connection to its HTTP/2 stack when it's a
// *crypto/tls.Conn — a utls connection never is, so handing it to
// http.Transport would silently downgrade to HTTP/1.1 even after the server
// agreed to HTTP/2 via ALPN.
type fingerprintedRoundTripper struct {
	helloID utls.ClientHelloID
	rootCAs *x509.CertPool // nil uses the system pool

	mu      sync.Mutex
	h2Conns map[string]*http2.ClientConn
	dialing map[string]*sync.Cond
}

// NewFingerprintedRoundTripper builds a RoundTripper that presents a Chrome
// TLS fingerprint on every handshake. Chrome is the least remarkable choice
// on the public internet. Exported (unlike v1's package-private
// newFingerprintedRoundTripper) because this is now a separate package
// from its caller (the httpClient wiring in cmd/gateway, once that exists).
func NewFingerprintedRoundTripper() *fingerprintedRoundTripper {
	return &fingerprintedRoundTripper{
		helloID: utls.HelloChrome_Auto,
		h2Conns: make(map[string]*http2.ClientConn),
		dialing: make(map[string]*sync.Cond),
	}
}

func (t *fingerprintedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		return nil, errors.New("tlsfingerprint: request has no URL")
	}
	if !strings.EqualFold(req.URL.Scheme, "https") {
		return nil, fmt.Errorf("tlsfingerprint: scheme %q is not supported, want https", req.URL.Scheme)
	}

	host := req.URL.Hostname()
	addr := canonicalAddr(req.URL)

	h2Conn, err := t.h2ConnFor(req.Context(), host, addr)
	if err != nil {
		return nil, err
	}
	if h2Conn != nil {
		resp, errRT := h2Conn.RoundTrip(req)
		if errRT != nil {
			t.dropH2Conn(addr, h2Conn)
			return nil, errRT
		}
		return resp, nil
	}

	// ALPN didn't select h2 for this host — single-use HTTP/1.1 exchange, no
	// keep-alive pool behind it. Every Google endpoint this gateway talks to
	// negotiates HTTP/2, so this path is a rarely-hit fallback, not the norm.
	return t.roundTripHTTP1(req, host, addr)
}

func (t *fingerprintedRoundTripper) dropH2Conn(addr string, conn *http2.ClientConn) {
	t.mu.Lock()
	if cached, ok := t.h2Conns[addr]; ok && cached == conn {
		delete(t.h2Conns, addr)
	}
	t.mu.Unlock()
}

// CloseIdleConnections implements the optional http.Transport behaviour used
// by http.Client.CloseIdleConnections.
func (t *fingerprintedRoundTripper) CloseIdleConnections() {
	t.mu.Lock()
	conns := make([]*http2.ClientConn, 0, len(t.h2Conns))
	for host, conn := range t.h2Conns {
		conns = append(conns, conn)
		delete(t.h2Conns, host)
	}
	t.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

func canonicalAddr(u *url.URL) string {
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(host, port)
}
