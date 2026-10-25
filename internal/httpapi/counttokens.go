// Part of the httpapi package — see errors.go for the package doc
// comment. POST /v1/messages/count_tokens — a faithful port of v1's
// handleCountTokens, using internal/tokens.WeightedRequestTokens (the
// typed equivalent of v1's estimateApproxClaudeTokens) instead of
// re-implementing the estimate on raw bytes.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"claudgravity/internal/config"
	"claudgravity/internal/ctxedit"
	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/ratelimit"
	"claudgravity/internal/tokens"

	"github.com/tidwall/gjson"
)

func handleCountTokens(cfgMgr *config.ConfigManager, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		WriteAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "This endpoint requires a POST request")
		return
	}
	if _, ok := authenticatedTenantFromRequest(cfgMgr, r); !ok {
		WriteAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid or missing API key")
		return
	}
	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 25*1024*1024))
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) || strings.Contains(err.Error(), "too large") {
			WriteAnthropicError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds 25MB limit")
			return
		}
		WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	// Matches /v1/messages' own validation: a non-empty "model" and a
	// non-empty "messages" array are both required as a hard 400, not
	// just Exists() (which a malformed {"messages": "a string"} or an
	// empty array would otherwise pass).
	messagesArr := gjson.GetBytes(bodyBytes, "messages")
	if !gjson.ValidBytes(bodyBytes) || !messagesArr.IsArray() || len(messagesArr.Array()) == 0 || gjson.GetBytes(bodyBytes, "model").String() == "" {
		WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON with a non-empty \"model\" and non-empty \"messages\"")
		return
	}

	if betaHeaders := r.Header.Values("anthropic-beta"); len(betaHeaders) > 0 {
		bodyBytes = mergeAnthropicBetaHeader(bodyBytes, strings.Join(betaHeaders, ","))
	}

	// context_management.edits are applied first (the spec: input_tokens is
	// the count AFTER editing, original_input_tokens the count before).
	ctxCfg, ctxErr := ctxedit.Parse(bodyBytes)
	if ctxErr != nil {
		WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", ctxErr.Error())
		return
	}
	originalTokens := 0
	if ctxCfg.Present {
		if res, err := ctxedit.Apply(bodyBytes, ctxCfg); err == nil {
			bodyBytes, originalTokens = res.Body, res.OriginalTokens
		}
	}

	var req claude.Request
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Request body must be valid JSON")
		return
	}
	estTokens := tokens.WeightedRequestTokens(req)
	resp := map[string]interface{}{
		"input_tokens": estTokens,
	}
	if ctxCfg.Present {
		if originalTokens < estTokens {
			originalTokens = estTokens
		}
		resp["context_management"] = map[string]interface{}{"original_input_tokens": originalTokens}
	}
	b, _ := json.Marshal(resp)
	// Every other successful response sets these (organization/workspace
	// ID, anthropic-ratelimit-*) — client-side quota-tracking tooling that
	// expects them on every response broke specifically on count_tokens
	// without this. No AccountRecord was ever selected for this endpoint
	// (it's a pure local estimate), so pass nil — ApplyHeaders already has
	// a synthetic-but-stable fallback for exactly that case.
	ratelimit.ApplyHeaders(w.Header(), nil, req.Model)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}
