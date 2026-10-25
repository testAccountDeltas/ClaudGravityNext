// Part of the mitm package's port from superrelay (v1)'s mitm.go — see
// ca.go for the split rationale and the frozen-behavior constraint.
package mitm

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"claudgravity/internal/logx"
	"claudgravity/internal/ssrfguard"
)

// IsAnthropicHost reports whether host is exactly one of the domains this
// gateway intercepts, or a subdomain of one — not merely a string that
// contains it anywhere. It strips any port and trailing dot before matching.
func IsAnthropicHost(host string) bool {
	// An audit caught that HandleConnectTunnel's own caller already
	// splits off the port before calling this (net.SplitHostPort at its
	// call site), but PassthroughToReal calls this directly on r.Host,
	// which can still carry a port (e.g. "claude.ai:443") — neither the
	// exact-match nor the suffix-match below ever accounts for one, so a
	// host string with its port still attached silently fails to match
	// at all, routing the request to the wrong destination
	// (api.anthropic.com instead of claude.ai, or vice versa). Strip a
	// port here unconditionally so every caller is correct regardless of
	// whether it already split one off itself.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	// A trailing "." (valid DNS syntax for "fully qualified") must not let
	// a client dodge this match and fall through to the plain tunnel path
	// below, where none of this gateway's response doctoring applies.
	host = strings.TrimSuffix(host, ".")
	for _, domain := range []string{"anthropic.com", "claude.ai"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

func isAnthropicHost(host string) bool {
	return IsAnthropicHost(host)
}

// checkConnectTarget reports whether host resolves to a public, safe
// address, per internal/ssrfguard's shared blocklist (loopback, private,
// link-local, CGNAT, reserved/class-E, cloud-metadata-reachable ranges).
// Kept as its own function (rather than calling ssrfguard.SafeResolve
// directly at the one call site below) because ssrf_characterization_test.go
// is one of this migration's frozen Phase-0 characterization tests and
// calls it by this exact name — see that test file's own header comment.
//
// It is a variable only so tests can point a tunnel at a loopback echo
// server; production code never reassigns it.
var checkConnectTarget = func(host string) ([]net.IP, error) {
	return ssrfguard.SafeResolve(host)
}

// AllowedConnectPorts returns the exact set of ports a CONNECT tunnel may
// target: the three standard ports every legitimate client needs, plus
// this gateway's OWN configured TLS API port — the literal fix for the
// live outage in commit 89de11b (see HandleConnectTunnel's comment just
// below). This is the SINGLE place that set is defined; HandleConnectTunnel
// below calls it rather than keeping its own inline copy, specifically so
// pkg/speccheck/rules/connectallowlist.go (speccheck rule 8,
// ConnectAllowlistSelfConsistency) can check it against config.json's
// live tls_port without forking a second copy of this list that could
// silently drift out of sync with the real enforcement logic.
func AllowedConnectPorts(localTLSPort int) []string {
	return []string{"443", "8443", "80", strconv.Itoa(localTLSPort)}
}

// HandleConnectTunnel implements native HTTP CONNECT proxying and TLS interception.
// proxySecret is required as a "Proxy-Authorization: Bearer <secret>" header
// on every CONNECT request — without it, the request is rejected with 407 to
// prevent open relay access to the internet.
func HandleConnectTunnel(w http.ResponseWriter, r *http.Request, ca *MitmCA, targetHandler http.Handler, proxySecret string, localTLSPort int) {
	if strings.TrimSpace(proxySecret) == "" || !ProxyAuthOK(r, proxySecret) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="SuperRelay Proxy"`)
		http.Error(w, "Proxy Authentication Required", http.StatusProxyAuthRequired)
		return
	}

	host := r.Host
	port := "443"
	if h, p, err := net.SplitHostPort(r.Host); err == nil {
		host = h
		port = p
	}

	// An audit caught that the only destination check here was the
	// private/internal-IP SSRF guard below — a client that knows (or
	// defeats) the proxy secret could CONNECT to a PUBLIC IP on a
	// non-HTTPS port (SMTP 25, SSH 22, a database port) and get a raw
	// TCP tunnel to it. This gateway's own documented job (see the
	// comment just below) is HTTPS/HTTP proxying for package registries
	// and APIs, never an arbitrary TCP relay — restrict to the standard
	// ports that use actually needs, plus this gateway's own configured
	// TLS API port: a client using ANTHROPIC_BASE_URL=https://<host>:<tls_port>
	// with HTTPS_PROXY pointed at this same gateway legitimately CONNECTs
	// to its own :tls_port (e.g. 8900), which caused a live outage
	// (ERR_PROXY_TUNNEL) the first time this restriction shipped.
	allowed := false
	for _, p := range AllowedConnectPorts(localTLSPort) {
		if p == port {
			allowed = true
			break
		}
	}
	if !allowed {
		logx.LogGateway("[SuperRelay Gateway] Refused CONNECT to non-standard port %s:%s", host, port)
		http.Error(w, "Tunnel to non-standard port forbidden", http.StatusForbidden)
		return
	}

	// This gateway's whole job is proxying to public hosts
	// (api.anthropic.com, github.com, npm/pypi registries, ...) — it has
	// no legitimate reason to ever CONNECT to a private, link-local, or
	// loopback address, so a client that knows (or defeats) the proxy
	// secret can't use this as an SSRF pivot into the host's own internal
	// network or cloud metadata endpoint (169.254.169.254). Checked before
	// Hijack() so a normal HTTP error response still works here.
	var verifiedIPs []net.IP
	if !isAnthropicHost(host) {
		ips, err := checkConnectTarget(host)
		if err != nil {
			logx.LogGateway("[SuperRelay Gateway] Refused CONNECT to target %s: %v", host, err)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		verifiedIPs = ips
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "Hijacking not supported", http.StatusInternalServerError)
		return
	}
	rawConn, rw, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	// A client that pipelines its TLS ClientHello right behind the CONNECT
	// request (same TCP read) already has those bytes sitting in net/http's
	// internal bufio.Reader by the time Hijack() returns — discarding that
	// reader here would drop them forever, and the TLS handshake below would
	// then read from an empty, silent socket until it times out.
	var clientConn net.Conn = rawConn
	if rw != nil && rw.Reader != nil && rw.Reader.Buffered() > 0 {
		clientConn = &bufferedConn{Conn: rawConn, r: io.MultiReader(rw.Reader, rawConn)}
	}

	// 1. Intercept Anthropic and Claude API endpoints locally. isAnthropicHost
	// does an exact-or-subdomain match, not a bare substring one — a raw
	// strings.Contains would also match an unrelated host that merely
	// contains "anthropic.com"/"claude.ai" as a substring (e.g.
	// "anthropic.com.attacker.net" or some third party's
	// "notclaude.ai.example.org"), MITM'ing it with this gateway's own
	// self-signed cert instead of leaving it to PassthroughToReal untouched.
	if isAnthropicHost(host) {
		tlsCert, err := ca.GetCertificate(host)
		if err != nil {
			_, _ = clientConn.Write([]byte("HTTP/1.1 500 Internal Server Error\r\n\r\n"))
			clientConn.Close()
			return
		}
		_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		tlsConfig := &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
			NextProtos:   []string{"http/1.1"},
		}
		tlsConn := tls.Server(clientConn, tlsConfig)
		go serveInterceptedTLS(tlsConn, targetHandler)
		return
	}

	// 2. Transparent TCP tunnel for all other external hosts. Dial a
	// pre-verified IP (never re-resolving the hostname) to prevent TOCTOU
	// DNS rebinding SSRF attacks, falling through the rest of the verified
	// list if the first (IPv4-preferred) address is unreachable from this
	// host — e.g. no outbound IPv6 route even though the name also
	// resolves to an AAAA address.
	var targetConn net.Conn
	if len(verifiedIPs) == 0 {
		targetConn, err = net.DialTimeout("tcp", r.Host, 10*time.Second)
	} else {
		// A Round 15 audit caught that every candidate IP got the full
		// 10s timeout regardless of how many there were — a dual-stack
		// host whose first-tried (IPv4-preferred, per the sort above)
		// address happens to be unreachable from this host still blocked
		// for the full 10s before ever trying the next one, so a client
		// could wait up to 10s * len(verifiedIPs) for a tunnel that a
		// single working address would have opened in milliseconds.
		// Shorten the per-attempt timeout once there's more than one
		// candidate to fall through to — similar in spirit to Happy
		// Eyeballs (RFC 8305) without the full parallel-dial machinery.
		perAttemptTimeout := 10 * time.Second
		if len(verifiedIPs) > 1 {
			perAttemptTimeout = 2500 * time.Millisecond
		}
		for i, ip := range verifiedIPs {
			attemptTimeout := perAttemptTimeout
			if i == len(verifiedIPs)-1 {
				// Give the LAST candidate the full timeout — if every
				// earlier address failed fast (refused/unreachable, not
				// merely slow), there's no reason to also rush the one
				// remaining attempt.
				attemptTimeout = 10 * time.Second
			}
			targetConn, err = net.DialTimeout("tcp", net.JoinHostPort(ip.String(), port), attemptTimeout)
			if err == nil {
				break
			}
		}
	}
	if err != nil {
		logx.LogGateway("[SuperRelay Gateway] Failed to dial tunnel target %s:%s: %v", host, port, err)
		_, _ = clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		clientConn.Close()
		return
	}

	// Connection successfully established to remote target
	_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	// Guard against either side going silent forever and leaking the
	// goroutines/fds indefinitely (e.g. a client that opens a tunnel and
	// never sends anything) — but net.Conn.SetDeadline is an ABSOLUTE
	// deadline, not an idle timeout: it is never reset by Read/Write, so a
	// single SetDeadline call before io.Copy killed any tunnel still
	// actively transferring data (git clone of a large repo, npm install,
	// downloading model weights) the instant 10 minutes of wall-clock time
	// passed, no matter how busy the connection was. idleCopy resets the
	// deadline before every read instead.
	const idleDeadline = 10 * time.Minute
	go func() {
		defer clientConn.Close()
		defer targetConn.Close()
		idleCopy(targetConn, clientConn, idleDeadline)
	}()
	go func() {
		defer clientConn.Close()
		defer targetConn.Close()
		idleCopy(clientConn, targetConn, idleDeadline)
	}()
}
