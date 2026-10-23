// Part of the httpapi package — see errors.go for the package doc
// comment. CORS preflight answering, response header stamping
// (anthropic-version/request-id/organization-id/workspace-id), and the
// panic-recovery wrapper — a faithful port of v1's commonAPIMiddleware/
// recoverMiddleware/nextRequestID.
package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"claudgravity/internal/fakeids"
	"claudgravity/internal/logx"
)

// fakeOrgID/fakeWorkspaceID alias internal/fakeids' package-level
// constants (already extracted during the internal/orchestrate port) —
// kept under these shorter package-local names since they're referenced
// at nearly every response-writing site in this package, same as v1's
// own bare package-level names.
const (
	fakeOrgID       = fakeids.OrgID
	fakeWorkspaceID = fakeids.WorkspaceID
)

var requestIDCounter uint64

// nextRequestID mints an id in the shape of a real Anthropic request-id
// (req_<hex>) — never parsed by clients, just expected to be present and
// look like one.
func nextRequestID() string {
	n := atomic.AddUint64(&requestIDCounter, 1)
	h := sha256.Sum256([]byte(fmt.Sprintf("%d-%d", time.Now().UnixNano(), n)))
	return "req_" + hex.EncodeToString(h[:12])
}

// CommonAPIMiddleware answers browser/Electron CORS preflight (OPTIONS)
// requests and stamps every response with anthropic-version/request-id —
// both expected unconditionally by the official Anthropic SDKs and by any
// web/Electron-based client.
func CommonAPIMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, DELETE, PUT, PATCH")
		h.Set("anthropic-organization-id", fakeOrgID)
		h.Set("anthropic-workspace-id", fakeWorkspaceID)
		// GAP-D1-11 & GAP-17: the official Anthropic SDKs (Stainless-generated,
		// used by browser/Electron clients) send their own tracing/client/idempotency
		// headers on every request — without allow-listing them here,
		// the browser's own CORS preflight blocks the request before it
		// ever reaches this gateway.
		h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version, anthropic-beta, anthropic-dangerous-direct-browser-access, x-claude-code-request-class, x-claude-code-agent-type, x-claude-code-agent-id, x-claude-code-prompt-id, x-session-id, x-claude-code-session-id, x-stainless-retry-count, x-stainless-timeout, x-stainless-lang, x-stainless-package-version, x-stainless-os, x-stainless-arch, x-stainless-runtime, x-stainless-runtime-version, x-stainless-async, anthropic-client-platform, anthropic-client-version, sentry-trace, baggage, traceparent, idempotency-key, anthropic-idempotency-key")
		h.Set("Access-Control-Max-Age", "86400")
		// GAP-D1-16: some CDNs/clients look for x-request-id instead of
		// (or alongside) Anthropic's own request-id.
		h.Set("Access-Control-Expose-Headers", "anthropic-version, request-id, x-request-id, retry-after, anthropic-organization-id, anthropic-workspace-id, anthropic-ratelimit-requests-limit, anthropic-ratelimit-requests-remaining, anthropic-ratelimit-requests-reset, anthropic-ratelimit-tokens-limit, anthropic-ratelimit-tokens-remaining, anthropic-ratelimit-tokens-reset, anthropic-ratelimit-input-tokens-limit, anthropic-ratelimit-input-tokens-remaining, anthropic-ratelimit-input-tokens-reset, anthropic-ratelimit-output-tokens-limit, anthropic-ratelimit-output-tokens-remaining, anthropic-ratelimit-output-tokens-reset, anthropic-ratelimit-unified-5h-status, anthropic-ratelimit-unified-5h-utilization, anthropic-ratelimit-unified-5h-reset, anthropic-ratelimit-unified-7d-status, anthropic-ratelimit-unified-7d-utilization, anthropic-ratelimit-unified-7d-reset, anthropic-ratelimit-unified-status, anthropic-ratelimit-unified-representative-claim, anthropic-ratelimit-unified-reset, anthropic-ratelimit-unified-overage-status, anthropic-ratelimit-unified-overage-disabled-reason")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.Set("anthropic-version", "2023-06-01")
		if h.Get("request-id") == "" {
			rid := r.Header.Get("x-request-id")
			if rid == "" {
				rid = r.Header.Get("request-id")
			}
			if rid == "" {
				rid = nextRequestID()
			}
			h.Set("request-id", rid)
			h.Set("x-request-id", rid)
		}
		next.ServeHTTP(w, r)
	})
}

// RecoverMiddleware stops a panic in any single request from crashing
// the whole process.
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				logx.LogGateway("[httpapi] PANIC recovered on %s %s: %v", r.Method, r.URL.Path, rec)
				// "internal_error" isn't one of Anthropic's documented
				// error types — a client that switches on the type string
				// falls through to its generic/unknown-error handling
				// instead of api_error's.
				WriteAnthropicError(w, http.StatusInternalServerError, "api_error", "internal gateway error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// recoverMiddleware is kept as an alias for backwards compatibility.
var recoverMiddleware = RecoverMiddleware
