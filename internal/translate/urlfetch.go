// Part of the translate package — see images.go for the doc comment on
// the image/document attachment paths this supports. GAP-T9 (per
// AUDIT_CLAUDE_TO_GEMINI_100PCT_SPEC.md): a source.type=="url" attachment
// used to always degrade to a "[Attached ... by URL: <url>]" text
// placeholder, with images.go's own comment admitting "we don't fetch
// arbitrary URLs ourselves" — leaving every URL-sourced image/document a
// model can never actually see. This file does fetch it now, but ONLY
// after internal/ssrfguard clears the resolved target: a model's tool
// input is attacker-controllable text (a malicious document, a prompt
// injection, a compromised MCP tool result), so fetching a URL it names is
// exactly the SSRF shape internal/mitm/tunnel.go already defends against
// for CONNECT targets — this reuses that same blocklist rather than
// opening a second way to reach an internal service or cloud metadata
// endpoint.
package translate

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"claudgravity/internal/ssrfguard"
)

// urlFetchMaxBytes caps how much of a remote attachment this gateway will
// read into memory — a deliberately small ceiling (Claude's own direct
// base64 attachments go through the same eventual translate pipeline with
// no special allowance for larger ones, and an unbounded read here would
// let a malicious URL exhaust gateway memory).
const urlFetchMaxBytes = 20 * 1024 * 1024

// maxURLFetchRedirects bounds how many redirect hops fetchURLAttachment
// follows — redirects are safe to follow here (every hop's target still
// goes through urlFetchClient's own DialContext/ssrfguard check below,
// exactly like the original URL), this cap only exists to stop a
// malicious or misconfigured server from redirecting forever.
const maxURLFetchRedirects = 5

// urlFetchClient dials by the EXACT IP ssrfguard.SafeResolve already
// verified, not by re-resolving the hostname a second time — GAP-U1: the
// original version resolved+checked host, then dialed net.JoinHostPort
// (host, port) again with the hostname string, which makes the Go
// resolver look host up a SECOND time to actually connect. An attacker
// controlling that DNS name can answer the first lookup (the one
// SafeResolve sees) with a safe public IP and the second one (the one
// actually dialed) with an internal address — classic DNS-rebinding
// TOCTOU — bypassing the check entirely. Dialing the already-verified IP
// directly closes that window; TLS server-name verification for https
// still uses the original hostname (Go's Transport derives ServerName
// from the dial address string passed in, not from what IP was actually
// connected), so this doesn't weaken certificate checking.
var urlFetchClient = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := ssrfguard.SafeResolve(host)
			if err != nil {
				return nil, err
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	},
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// GAP-U2: refusing every redirect outright broke the common case
		// of an image/document hosted on S3/a CDN/GitHub, which routinely
		// redirect at least once. Each redirect target still has to clear
		// the SAME SSRF check above (DialContext runs on every hop, not
		// just the first), so allowing a bounded number of them doesn't
		// reopen GAP-U1 — it only risks a long chain, which the count
		// check below stops.
		if len(via) >= maxURLFetchRedirects {
			return fmt.Errorf("stopped after %d redirects", maxURLFetchRedirects)
		}
		return nil
	},
}

// fetchURLAttachment fetches rawURL and returns its raw bytes and the
// media type to treat it as (preferring the server's Content-Type over
// any caller guess), or ok==false if the URL is unsafe, unreachable, not
// http(s), or too large. Only http/https are ever attempted — a client
// asking for "file://" or any other scheme gets the same safe refusal as
// an SSRF-blocked host.
func fetchURLAttachment(rawURL string) (data []byte, mediaType string, ok bool) {
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return nil, "", false
	}
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", false
	}
	resp, err := urlFetchClient.Do(req)
	if err != nil {
		return nil, "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, urlFetchMaxBytes+1))
	if err != nil || int64(len(body)) > urlFetchMaxBytes {
		return nil, "", false
	}
	return body, resp.Header.Get("Content-Type"), true
}
