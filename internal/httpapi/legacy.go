// Part of the httpapi package — see errors.go for the package doc
// comment. These three handlers are explicitly named "dead stubs" here,
// same as v1's own comments: none implements a real backend, they just
// answer with a clear Anthropic-shaped error instead of falling through
// to whatever else happens to be registered on "/" (v1's dashboard
// catch-all, which demanded HTTP Basic Auth and returned an HTML/
// text-plain 401 instead of a proper JSON error envelope).
package httpapi

import (
	"fmt"
	"net/http"
	"strings"
)

// handleDeprecatedComplete answers the legacy Anthropic text-completions
// endpoint (pre-Messages-API) with a clear, actionable error.
func handleDeprecatedComplete(w http.ResponseWriter, r *http.Request) {
	WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "The /v1/complete endpoint is deprecated and not supported by this gateway; use /v1/messages instead.")
}

// handleCacheTouch answers POST /v1/messages/cache_touch — a lightweight
// ping Claude Code uses to extend the Anthropic prompt-cache ephemeral TTL.
func handleCacheTouch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		WriteAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "Method Not Allowed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("anthropic-version", "2023-06-01")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"status":"ok","touched":true}`))
}

// handleUnsupportedBatches answers the Message Batches API with a clear
// "not supported" — this gateway has no batch-processing backend.
// Intercepts all batch subpaths (/v1/messages/batches, /v1/messages/batches/,
// /v1/messages/batches/{id}, /v1/messages/batches/{id}/results) returning a
// structured 400 error instead of falling through to 404.
func handleUnsupportedBatches(w http.ResponseWriter, r *http.Request) {
	WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Message batches are not supported by this gateway.")
}

// handleUnknownV1Path is the last-resort fallback for any /v1/* path not
// matched by a more specific handler (registered last on the mux, so Go's
// ServeMux only reaches it when nothing more specific matched).
func handleUnknownV1Path(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/messages/batches") {
		handleUnsupportedBatches(w, r)
		return
	}
	WriteAnthropicError(w, http.StatusNotFound, "not_found_error", fmt.Sprintf("Unknown endpoint: %s %s", r.Method, r.URL.Path))
}
