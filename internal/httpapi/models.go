// Part of the httpapi package — see errors.go for the package doc
// comment. GET /v1/models and /v1/models/{id} — a faithful port of v1's
// knownModels/allModels/handleModels/handleModelByID.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
)

// buildModelCapabilities builds a capability tree matching Anthropic Models API schema,
// where capabilities is a typed hierarchical object rather than a flat map.
func buildModelCapabilities(thinking, vision bool) map[string]interface{} {
	caps := map[string]interface{}{
		"tools":              true,
		"image_input":        map[string]interface{}{"supported": vision},
		"structured_outputs": map[string]interface{}{"supported": true},
	}
	if thinking {
		caps["thinking"] = map[string]interface{}{
			"supported": true,
			"types": map[string]interface{}{
				"enabled":  map[string]interface{}{"supported": true},
				"adaptive": map[string]interface{}{"supported": true},
			},
		}
		caps["effort"] = map[string]interface{}{
			"supported": true,
			"low":       map[string]interface{}{"supported": true},
			"medium":    map[string]interface{}{"supported": true},
			"high":      map[string]interface{}{"supported": true},
			"xhigh":     map[string]interface{}{"supported": true},
			"max":       map[string]interface{}{"supported": true},
		}
	} else {
		caps["thinking"] = map[string]interface{}{"supported": false}
	}
	return caps
}

// knownModels backs both /v1/models (list) and /v1/models/{id} (lookup).
// "type": "model" (not "object": "model") and "created_at" match the
// real Anthropic Models API schema — the official SDKs validate against
// that schema and raise a deserialization error on an OpenAI-style
// "object" field instead.
var knownModels = []map[string]interface{}{
	{"id": "gemini-2.5-flash", "type": "model", "display_name": "Gemini 2.5 Flash", "created_at": "2025-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "gemini-2.5-pro", "type": "model", "display_name": "Gemini 2.5 Pro", "created_at": "2025-01-01T00:00:00Z", "max_input_tokens": 2097152, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "gemini-3.8-flash-tiered", "type": "model", "display_name": "Gemini 3.8 Flash Tiered", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-3-haiku-20240307", "type": "model", "display_name": "Claude 3 Haiku", "created_at": "2024-03-07T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 4096, "capabilities": buildModelCapabilities(false, true)},
	{"id": "claude-3-5-haiku-20241022", "type": "model", "display_name": "Claude 3.5 Haiku", "created_at": "2024-10-22T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 8192, "capabilities": buildModelCapabilities(false, true)},
	{"id": "claude-3-5-haiku", "type": "model", "display_name": "Claude 3.5 Haiku", "created_at": "2024-10-22T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 8192, "capabilities": buildModelCapabilities(false, true)},
	{"id": "claude-3-5-haiku-latest", "type": "model", "display_name": "Claude 3.5 Haiku", "created_at": "2024-10-22T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 8192, "capabilities": buildModelCapabilities(false, true)},
	{"id": "claude-3-5-sonnet-20241022", "type": "model", "display_name": "Claude 3.5 Sonnet", "created_at": "2024-10-22T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 8192, "capabilities": buildModelCapabilities(false, true)},
	{"id": "claude-3-5-sonnet", "type": "model", "display_name": "Claude 3.5 Sonnet", "created_at": "2024-10-22T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 8192, "capabilities": buildModelCapabilities(false, true)},
	{"id": "claude-3-5-sonnet-latest", "type": "model", "display_name": "Claude 3.5 Sonnet", "created_at": "2024-10-22T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 8192, "capabilities": buildModelCapabilities(false, true)},
	{"id": "claude-3-7-sonnet-20250219", "type": "model", "display_name": "Claude 3.7 Sonnet", "created_at": "2025-02-19T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-3-7-sonnet", "type": "model", "display_name": "Claude 3.7 Sonnet", "created_at": "2025-02-19T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-3-7-sonnet-latest", "type": "model", "display_name": "Claude 3.7 Sonnet", "created_at": "2025-02-19T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-3-opus-20240229", "type": "model", "display_name": "Claude 3 Opus", "created_at": "2024-02-29T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 4096, "capabilities": buildModelCapabilities(false, true)},
	// GAP-MOD2: an old pinned SDK config requesting this exact id 404'd.
	{"id": "claude-3-sonnet-20240229", "type": "model", "display_name": "Claude 3 Sonnet", "created_at": "2024-02-29T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 4096, "capabilities": buildModelCapabilities(false, true)},
	{"id": "claude-3-opus-latest", "type": "model", "display_name": "Claude 3 Opus", "created_at": "2024-02-29T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 4096, "capabilities": buildModelCapabilities(false, true)},
	// GAP-O4: claude-opus-4-0 (the canonical id some older/pinned SDK
	// configs still request) 404'd on both /v1/models/claude-opus-4-0 and
	// tiering — the HasPrefix("claude-opus") match in tiering.go already
	// routes it correctly, this entry only closes the catalog-lookup gap.
	{"id": "claude-opus-4-0", "type": "model", "display_name": "Claude Opus 4", "created_at": "2025-02-01T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-opus-4-6", "type": "model", "display_name": "Claude Opus 4.6", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-opus-4-6-thinking", "type": "model", "display_name": "Claude Opus 4.6 Thinking", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-opus-4-5", "type": "model", "display_name": "Claude Opus 4.5", "created_at": "2025-11-01T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-opus-4-7", "type": "model", "display_name": "Claude Opus 4.7", "created_at": "2025-12-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-opus-4-8", "type": "model", "display_name": "Claude Opus 4.8", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-sonnet-4-5", "type": "model", "display_name": "Claude Sonnet 4.5", "created_at": "2025-09-29T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-haiku-4-5", "type": "model", "display_name": "Claude Haiku 4.5", "created_at": "2025-10-01T00:00:00Z", "max_input_tokens": 200000, "max_tokens": 64000, "capabilities": buildModelCapabilities(false, true)},
	{"id": "claude-sonnet-4-6", "type": "model", "display_name": "Claude Sonnet 4.6", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-opus-5", "type": "model", "display_name": "Claude Opus 5", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 128000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-opus-5-5", "type": "model", "display_name": "Claude Opus 5.5", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 128000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-sonnet-5", "type": "model", "display_name": "Claude Sonnet 5", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 128000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-sonnet-5-5", "type": "model", "display_name": "Claude Sonnet 5.5", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 128000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-haiku-5-5", "type": "model", "display_name": "Claude Haiku 5.5", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-fable-5", "type": "model", "display_name": "Claude Fable 5", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 128000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-fable-5-1", "type": "model", "display_name": "Claude Fable 5.1", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 128000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-mythos-5", "type": "model", "display_name": "Claude Mythos 5", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 128000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "claude-mythos-5-1", "type": "model", "display_name": "Claude Mythos 5.1", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 128000, "capabilities": buildModelCapabilities(true, true)},
	// Claude Code's Agent/Task tool can request a subagent model by its
	// bare alias ("haiku", "sonnet", "opus", "fable", "mythos" — no
	// "claude-" prefix; internal/accounts/tiering already maps these to a
	// real Gemini target), but a lookup by full canonical id alone 404s on
	// exactly the short names Claude Code itself sends.
	{"id": "haiku", "type": "model", "display_name": "Claude Haiku (alias)", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(false, true)},
	{"id": "sonnet", "type": "model", "display_name": "Claude Sonnet (alias)", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "opus", "type": "model", "display_name": "Claude Opus (alias)", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "fable", "type": "model", "display_name": "Claude Fable (alias)", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
	{"id": "mythos", "type": "model", "display_name": "Claude Mythos (alias)", "created_at": "2026-01-01T00:00:00Z", "max_input_tokens": 1048576, "max_tokens": 64000, "capabilities": buildModelCapabilities(true, true)},
}

// allModels merges the static Claude-alias/canonical-Gemini-name list
// with whatever real Gemini sub-models the live catalog actually fetched
// from Google at startup (gemini-3.8-flash-high/-medium/-low/-tiered
// etc.) — a client asking about one of tiering's own real target models
// must not 404 for a model this gateway is actively routing requests to.
func allModels() []map[string]interface{} {
	seen := make(map[string]bool, len(knownModels))
	out := make([]map[string]interface{}, 0, len(knownModels))
	for _, m := range knownModels {
		out = append(out, m)
		seen[m["id"].(string)] = true
	}
	for _, m := range accounts.ModelCatalog().GetAll() {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		entry := map[string]interface{}{
			"id":           m.ID,
			"type":         "model",
			"display_name": m.DisplayName,
			"created_at":   "2026-01-01T00:00:00Z",
			"capabilities": buildModelCapabilities(m.Thinking, true),
		}
		if m.MaxTokens > 0 {
			entry["max_input_tokens"] = m.MaxTokens
		}
		if m.MaxOutput > 0 {
			entry["max_tokens"] = m.MaxOutput
		}
		out = append(out, entry)
	}
	return out
}

// authenticatedTenantFromRequest resolves the tenant from either
// x-api-key or Authorization: Bearer — shared by every GET endpoint in
// this package that only needs to confirm SOME valid tenant, not any of
// its fields.
func authenticatedTenantFromRequest(cfgMgr *config.ConfigManager, r *http.Request) (config.TenantConfig, bool) {
	if cfgMgr == nil {
		return config.TenantConfig{}, false
	}
	clientKey := r.Header.Get("x-api-key")
	if clientKey == "" {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			clientKey = strings.TrimSpace(authHeader[7:])
		} else {
			clientKey = authHeader
		}
	}
	if strings.TrimSpace(clientKey) == "" {
		return config.TenantConfig{}, false
	}
	return cfgMgr.GetTenant(clientKey)
}

func handleModels(cfgMgr *config.ConfigManager, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		WriteAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "This endpoint requires a GET request")
		return
	}
	if _, ok := authenticatedTenantFromRequest(cfgMgr, r); !ok {
		WriteAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid or missing API key")
		return
	}
	q := r.URL.Query()
	page, hasMore, errMsg := paginateModels(sortedModels(), q.Get("limit"), q.Get("after_id"), q.Get("before_id"))
	if errMsg != "" {
		WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", errMsg)
		return
	}
	data := make([]map[string]interface{}, len(page))
	for i, m := range page {
		data[i] = decorateModel(m)
	}
	resp := map[string]interface{}{
		"data":     data,
		"has_more": hasMore,
		"first_id": nil,
		"last_id":  nil,
	}
	if len(page) > 0 {
		resp["first_id"] = page[0]["id"]
		resp["last_id"] = page[len(page)-1]["id"]
	}
	b, _ := json.Marshal(resp)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

func handleModelByID(cfgMgr *config.ConfigManager, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		WriteAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "This endpoint requires a GET request")
		return
	}
	if _, ok := authenticatedTenantFromRequest(cfgMgr, r); !ok {
		WriteAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid or missing API key")
		return
	}
	id := r.PathValue("id")
	if m, ok := lookupModel(id); ok {
		b, _ := json.Marshal(m)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
		return
	}
	WriteAnthropicError(w, http.StatusNotFound, "not_found_error", fmt.Sprintf("model: %s", id))
}
