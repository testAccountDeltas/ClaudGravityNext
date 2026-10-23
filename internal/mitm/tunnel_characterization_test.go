// Phase 1 characterization test for the ported mitm package — the exact
// three CONNECT-allowlist freeze tests from
// superrelay/mitm_tlsfreeze_test.go (Phase 0 of this migration), proving
// HandleConnectTunnel's port is byte-for-byte behaviorally identical to
// v1, as required by Part 3 p.5 / Part 7.2 of the spec. The TLS-fingerprint
// profile test from that same v1 file is NOT here — it exercises
// tlsfingerprint.go, which this phase hasn't ported yet; it belongs in
// that package's own characterization test once it is.
package mitm

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestFreeze_ConnectAllowlistRejectsNonStandardPort locks in today's
// behavior: a CONNECT to any port outside {443, 8443, 80, <configured
// tls_port>} is refused with 403 before any network I/O happens. Uses
// "api.anthropic.com" so isAnthropicHost short-circuits the SSRF/DNS check
// entirely — this test exercises only the port switch, with zero real
// network access, by design.
func TestFreeze_ConnectAllowlistRejectsNonStandardPort(t *testing.T) {
	const testSecret = "test-proxy-secret"
	req := httptest.NewRequest(http.MethodConnect, "http://api.anthropic.com:22", nil)
	req.Host = "api.anthropic.com:22"
	req.Header.Set("Proxy-Authorization", "Bearer "+testSecret)
	rec := httptest.NewRecorder()

	HandleConnectTunnel(rec, req, nil, nil, testSecret, 8900)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("CONNECT to non-standard port 22 must be rejected with 403, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestFreeze_ConnectAllowlistAllowsConfiguredTLSPort is the literal
// regression test for superrelay's live incident (commit 89de11b): a
// CONNECT to the gateway's OWN configured tls_port must pass the
// port-allowlist gate, not be rejected as "non-standard". It can't
// proceed all the way through a real hijack in a unit test
// (httptest.ResponseRecorder doesn't implement http.Hijacker), so passing
// the gate is observed by getting "Hijacking not supported" (500) rather
// than "Tunnel to non-standard port forbidden" (403) — any 403 here is
// the exact regression this test exists to catch.
func TestFreeze_ConnectAllowlistAllowsConfiguredTLSPort(t *testing.T) {
	const configuredTLSPort = 8900
	const testSecret = "test-proxy-secret"
	req := httptest.NewRequest(http.MethodConnect, "http://api.anthropic.com:8900", nil)
	req.Host = "api.anthropic.com:8900"
	req.Header.Set("Proxy-Authorization", "Bearer "+testSecret)
	rec := httptest.NewRecorder()

	HandleConnectTunnel(rec, req, nil, nil, testSecret, configuredTLSPort)

	if rec.Code == http.StatusForbidden {
		t.Fatalf("CONNECT to the gateway's own configured tls_port (%d) must NOT be rejected — "+
			"this is the exact self-consistency regression fixed in superrelay commit 89de11b, got 403 body=%q",
			configuredTLSPort, rec.Body.String())
	}
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected the port gate to pass and fail later on hijack-not-supported (500) in this test harness, got %d body=%q", rec.Code, rec.Body.String())
	}
}

// TestFreeze_ConnectAllowlistStillAllowsStandardPorts cross-checks the
// other direction: standard ports 443/8443/80 must still be allowed
// regardless of whatever tls_port is configured — the allowlist is
// additive, not a replacement of the three standard ports.
func TestFreeze_ConnectAllowlistStillAllowsStandardPorts(t *testing.T) {
	const testSecret = "test-proxy-secret"
	for _, port := range []string{"443", "8443", "80"} {
		req := httptest.NewRequest(http.MethodConnect, "http://api.anthropic.com:"+port, nil)
		req.Host = "api.anthropic.com:" + port
		req.Header.Set("Proxy-Authorization", "Bearer "+testSecret)
		rec := httptest.NewRecorder()

		HandleConnectTunnel(rec, req, nil, nil, testSecret, 9999) // unrelated configured tls_port

		if rec.Code == http.StatusForbidden {
			t.Fatalf("standard port %s must be allowed regardless of configured tls_port, got 403 body=%q", port, rec.Body.String())
		}
	}
}

func TestHandleConnectTunnel_ProxyAuthFailClosed(t *testing.T) {
	// 1. Unconfigured / empty proxy secret rejects with 407
	req := httptest.NewRequest(http.MethodConnect, "http://api.anthropic.com:443", nil)
	req.Host = "api.anthropic.com:443"
	rec := httptest.NewRecorder()
	HandleConnectTunnel(rec, req, nil, nil, "", 8900)
	if rec.Code != http.StatusProxyAuthRequired {
		t.Fatalf("empty secret: got %d, want 407", rec.Code)
	}

	// 2. Whitespace-only proxy secret rejects with 407
	rec = httptest.NewRecorder()
	HandleConnectTunnel(rec, req, nil, nil, "   ", 8900)
	if rec.Code != http.StatusProxyAuthRequired {
		t.Fatalf("whitespace secret: got %d, want 407", rec.Code)
	}

	// 3. Configured secret with missing auth rejects with 407
	rec = httptest.NewRecorder()
	HandleConnectTunnel(rec, req, nil, nil, "correct-secret", 8900)
	if rec.Code != http.StatusProxyAuthRequired {
		t.Fatalf("missing auth: got %d, want 407", rec.Code)
	}

	// 4. Configured secret with wrong auth rejects with 407
	reqWrong := httptest.NewRequest(http.MethodConnect, "http://api.anthropic.com:443", nil)
	reqWrong.Host = "api.anthropic.com:443"
	reqWrong.Header.Set("Proxy-Authorization", "Bearer wrong-secret")
	rec = httptest.NewRecorder()
	HandleConnectTunnel(rec, reqWrong, nil, nil, "correct-secret", 8900)
	if rec.Code != http.StatusProxyAuthRequired {
		t.Fatalf("wrong auth: got %d, want 407", rec.Code)
	}
}
