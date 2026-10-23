// Part of the mitm package's port from superrelay (v1)'s mitm.go — see
// ca.go for the split rationale and the frozen-behavior constraint.
package mitm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/fakeids"
	"claudgravity/internal/logx"
)

// An audit caught that http.Client's Timeout bounds the ENTIRE exchange —
// connect, headers, AND the full response body read (per net/http's own
// docs) — so any passthrough request or stream that happened to run
// longer than 30s got killed with a 502 partway through, regardless of
// whether it was making real progress. PassthroughToReal exists
// specifically for real Anthropic endpoints this gateway hasn't
// implemented, which can legitimately be slow or long-running (a big
// upload, a long-lived stream). Bound only the "is anything ever going to
// respond at all" phase via the Transport's ResponseHeaderTimeout —
// once a response starts, let it run to completion.
var passthroughClient = &http.Client{
	Transport: &http.Transport{
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

var hopByHopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Transfer-Encoding": true,
	"Trailer": true, "Upgrade": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true,
}

// PassthroughToReal forwards a decrypted, intercepted request to the real
// host it was originally addressed to (api.anthropic.com, claude.ai, ...).
// Only /api/oauth/usage and /api/claude_cli/bootstrap are answered locally
// with fabricated data (they depend on a real subscription we don't have);
// every other path under these intercepted domains must reach the real
// servers untouched, or background features (marketplaces, mcp-registry,
// telemetry, first-run connectivity checks, ...) break with a local 404.
func PassthroughToReal(w http.ResponseWriter, r *http.Request) {
	target := *r.URL
	target.Scheme = "https"
	if isAnthropicHost(r.Host) {
		target.Host = r.Host
	} else {
		target.Host = "api.anthropic.com"
	}

	// r.Body is a one-shot io.ReadCloser; http.NewRequest only wires up
	// GetBody automatically for *bytes.Reader/*bytes.Buffer/*strings.Reader,
	// so passing it directly leaves GetBody nil and Go's transport can't
	// rewind-and-retry on a transient connection drop — confirmed live in
	// superrelay.log: "cannot rewind body after connection loss". Buffering
	// it (these are passthrough API calls, not large file transfers) lets
	// the transport retry transparently instead of surfacing a 502.
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "proxy error", http.StatusBadGateway)
		return
	}
	// An audit caught that plain http.NewRequest defaults the outbound
	// request's context to context.Background(), never linked to r's own
	// context — if the original client disconnects mid-request, this
	// passthrough fetch to the real Anthropic API had no way to find out
	// and kept running to completion regardless, wasting the outbound
	// connection and any real API quota for a response nobody will read.
	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(bodyBytes))
	if err != nil {
		http.Error(w, "proxy error", http.StatusBadGateway)
		return
	}
	outReq.Header = r.Header.Clone()
	for k := range hopByHopHeaders {
		outReq.Header.Del(k)
	}
	// Security: never forward the gateway's own tenant key / proxy secret to
	// the real host, and drop all credentials when the Host was not a real
	// Anthropic host (see scrub.go).
	scrubCredentialHeaders(outReq.Header, isAnthropicHost(r.Host))
	outHost := target.Host
	if h, _, err := net.SplitHostPort(outHost); err == nil {
		outHost = h
	}
	outReq.Host = outHost

	resp, err := passthroughClient.Do(outReq)
	if err != nil {
		logx.LogGateway("[SuperRelay Gateway] MITM passthrough to %s%s failed: %v", target.Host, r.URL.Path, err)
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// MitmResponseWriter.Flush() always buffers the whole body and computes
	// its own real Content-Length — but a chunked upstream response has no
	// Content-Length of its own, so blindly copying its hop-by-hop headers
	// through left the stale "Transfer-Encoding: chunked" sitting next to
	// Flush()'s freshly computed Content-Length: two conflicting framing
	// headers on the same response, which RFC 7230 §3.3.2 forbids and which
	// Node.js's strict HTTP/1.1 parser (what Claude Code's CLI runs on)
	// rejects outright instead of just picking one.
	for k, vv := range resp.Header {
		if hopByHopHeaders[k] {
			continue
		}
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	// An audit caught that a plain io.Copy here never flushes:
	// MitmResponseWriter.Write only appends to its internal buffer, and
	// nothing reaches the actual TLS connection until Flush() or Close()
	// runs — Close() only fires once the whole handler has returned, so a
	// real long-lived streaming response passed straight through to
	// claude.ai/api.anthropic.com (anything this gateway hasn't
	// implemented itself) was fully buffered until it finished, instead
	// of streaming to the client as it arrived. Copy manually and flush
	// after every chunk, the same http.Flusher pattern already used for
	// this gateway's own SSE responses.
	flusher, hasFlusher := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				break
			}
			if hasFlusher {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}
}

var requestIDCounter uint64

// nextRequestID mints an id shaped like a real Anthropic request-id.
func nextRequestID() string {
	n := atomic.AddUint64(&requestIDCounter, 1)
	h := sha256.Sum256([]byte(fmt.Sprintf("bootstrap-%d-%d", time.Now().UnixNano(), n)))
	return "req_" + hex.EncodeToString(h[:12])
}

// staticBootstrapModels lists static Claude models, aliases, Gemini models,
// and dated IDs to ensure CLI v2.1+ entitlement validation passes.
var staticBootstrapModels = []string{
	// Static aliases
	"opus", "sonnet", "haiku", "fable", "mythos",
	// Claude 5 series
	"claude-opus-5-5", "claude-opus-5",
	"claude-sonnet-5-5", "claude-sonnet-5",
	"claude-haiku-5-5", "claude-haiku-4-5",
	"claude-fable-5", "claude-fable-5-1",
	"claude-mythos-5", "claude-mythos-5-1",
	// Claude 4 series
	"claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6", "claude-opus-4-5", "claude-opus-4-0",
	"claude-opus-4-6-thinking",
	"claude-sonnet-4-6", "claude-sonnet-4-5",
	// Claude 3 series and dated variants
	"claude-3-7-sonnet", "claude-3-7-sonnet-20250219", "claude-3-7-sonnet-latest",
	"claude-3-5-sonnet", "claude-3-5-sonnet-20241022", "claude-3-5-sonnet-latest",
	"claude-3-5-haiku", "claude-3-5-haiku-20241022", "claude-3-5-haiku-latest",
	"claude-3-haiku-20240307", "claude-3-sonnet-20240229",
	"claude-3-opus-20240229", "claude-3-opus-latest",
	// Gemini models
	"gemini-2.5-pro", "gemini-2.5-flash", "gemini-3.8-flash-tiered",
	// Other supported models/aliases
	"gpt-oss-120b-medium",
}

// HandleBootstrapSpoof handles GET /api/claude_cli/bootstrap
func HandleBootstrapSpoof(w http.ResponseWriter, r *http.Request) {
	logx.LogGateway("[SuperRelay Gateway] >>> Intercepted GET /api/claude_cli/bootstrap <<<")

	rid := w.Header().Get("request-id")
	if rid == "" {
		rid = w.Header().Get("x-request-id")
	}
	if rid == "" && r != nil {
		rid = r.Header.Get("x-request-id")
		if rid == "" {
			rid = r.Header.Get("request-id")
		}
	}
	if rid == "" {
		rid = nextRequestID()
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("anthropic-organization-id", fakeids.OrgID)
	w.Header().Set("anthropic-workspace-id", fakeids.WorkspaceID)
	w.Header().Set("anthropic-version", "2023-06-01")
	w.Header().Set("request-id", rid)
	w.Header().Set("x-request-id", rid)
	w.WriteHeader(http.StatusOK)

	seen := make(map[string]bool)
	var modelAccess []map[string]interface{}
	for _, m := range staticBootstrapModels {
		if !seen[m] {
			seen[m] = true
			modelAccess = append(modelAccess, map[string]interface{}{
				"api_name": m,
				"entitled": true,
			})
		}
	}
	for _, m := range accounts.ModelCatalog().GetAll() {
		if m.ID != "" && !seen[m.ID] {
			seen[m.ID] = true
			modelAccess = append(modelAccess, map[string]interface{}{
				"api_name": m.ID,
				"entitled": true,
			})
		}
	}

	resp := map[string]interface{}{
		"org_model_default": map[string]interface{}{
			"name":                    "claude-opus-5-5",
			"updated_at":              "2026-01-01T00:00:00Z",
			"data_source":             "org",
			"override_user_selection": false,
		},
		"oauth_account": map[string]interface{}{
			// An audit caught this hardcoded the SAME literal UUID as
			// fakeOrgID below it, for a different entity (the account,
			// not the organization) — now uses the distinct fakeAccountID,
			// consistent with the oauth profile/validate responses.
			"account_uuid":                 fakeids.AccountID,
			"account_email":                "user@claudgravity.local",
			"organization_uuid":            fakeids.OrgID,
			"organization_name":            "ClaudGravity Workspace",
			"organization_type":            "claude_enterprise",
			"organization_rate_limit_tier": "tier_4",
			"user_rate_limit_tier":         "tier_4",
			"seat_tier":                    "enterprise",
		},
		"additional_model_options": []interface{}{},
		"model_access":             modelAccess,
	}
	_ = json.NewEncoder(w).Encode(resp)
}
