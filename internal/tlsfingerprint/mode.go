// Part of the tlsfingerprint package. Everything this gateway sends to Google
// claims to be one client: "vscode/1.X.X (Antigravity/<version>)", whose CloudCode
// calls are made by a Go program (the language server). The matching TLS
// ClientHello is Go's own. The browser-shaped ClientHello of the fingerprinted
// round tripper (uTLS Chrome) next to that User-Agent is a combination no real
// client produces, and a ClientHello has to match the client the rest of the
// request claims to be. The reference relay (clirelay) applies browser mimicry
// to a different provider only and sends Antigravity traffic over the standard
// Go transport; so does this package by default now. "chrome" selects the
// previous behaviour.
package tlsfingerprint

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

// Modes accepted by SetMode.
const (
	ModeGo     = "go"     // the standard Go transport (default)
	ModeChrome = "chrome" // uTLS Chrome ClientHello (the previous behaviour)
)

var (
	modeMu sync.RWMutex
	mode   = ModeGo
)

// SetMode selects the transport that NewGoogleTransport builds. Call it before
// the first request leaves; unknown values select the default.
func SetMode(m string) {
	if m != ModeChrome {
		m = ModeGo
	}
	modeMu.Lock()
	mode = m
	modeMu.Unlock()
}

// Mode reports the selected mode.
func Mode() string {
	modeMu.RLock()
	defer modeMu.RUnlock()
	return mode
}

// newStandardTransport is net/http's transport with HTTP/2 health checks, the
// same connection-liveness bounds the fingerprinted transport uses.
func newStandardTransport() http.RoundTripper {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: fingerprintDialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   fingerprintDialTimeout,
		IdleConnTimeout:       h2IdleConnTimeout,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		ExpectContinueTimeout: time.Second,
	}
	if t2, err := http2.ConfigureTransports(tr); err == nil {
		t2.ReadIdleTimeout = h2ReadIdleTimeout
		t2.PingTimeout = h2PingTimeout
	}
	return tr
}

// deferredTransport builds the real transport on the first request, so a
// package-level http.Client created at import time still honours a mode set
// afterwards from the configuration.
type deferredTransport struct {
	once sync.Once
	rt   http.RoundTripper
}

func (d *deferredTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	d.once.Do(func() {
		if Mode() == ModeChrome {
			d.rt = NewFingerprintedRoundTripper()
		} else {
			d.rt = newStandardTransport()
		}
	})
	return d.rt.RoundTrip(req)
}

// NewGoogleTransport returns the round tripper for traffic to Google's
// endpoints (CloudCode, OAuth, userinfo).
func NewGoogleTransport() http.RoundTripper { return &deferredTransport{} }
