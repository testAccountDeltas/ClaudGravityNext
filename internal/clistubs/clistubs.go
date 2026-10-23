// Package clistubs answers the Claude Code CLI's own control-endpoint
// traffic — account/org identity, settings, policy limits, telemetry,
// prompt-cache keepalive — none of which is part of the Anthropic
// Messages API surface internal/httpapi owns. GAP-P0-02: these paths were
// never registered anywhere in this binary (confirmed by a repo-wide
// grep turning up zero matches), so they fell through to the dashboard's
// catch-all "/" handler (HTTP Basic Auth, a 401 for an unauthenticated
// CLI) or to httpapi's own /v1/ catch-all (a clean but still-fatal 404
// for /v1/messages/cache_touch). A 401/404 on cache_touch specifically
// is not just one warning: Claude Code permanently disables the
// background prompt-cache keepalive for the rest of the session after a
// few consecutive failures.
//
// Faithfully ported from v1 (superrelay/server.go)'s equivalent closures
// — the response shapes below were hardened across many real audits
// there (missing memberships array, flat vs. nested uuid fields, a
// shared organization/account UUID two different entities both got
// cached under), so they're copied as-is rather than re-derived from the
// Anthropic API docs.
package clistubs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"claudgravity/internal/fakeids"
	"claudgravity/internal/mitm"
	"claudgravity/internal/ratelimit"
)

var requestIDCounter uint64

// nextRequestID mints an id shaped like a real Anthropic request-id —
// same scheme as internal/httpapi's own (unexported, so not reused
// directly) nextRequestID, duplicated here rather than exported across
// the package boundary for one small helper.
func nextRequestID() string {
	n := atomic.AddUint64(&requestIDCounter, 1)
	h := sha256.Sum256([]byte(fmt.Sprintf("clistubs-%d-%d", time.Now().UnixNano(), n)))
	return "req_" + hex.EncodeToString(h[:12])
}

// GAP-CS-03: this used to set only Content-Type and the org/workspace
// IDs — anthropic-version and request-id are expected unconditionally
// by the official Anthropic SDKs on every response, not just
// /v1/messages, and nothing wrapping this package's mux adds them for
// these paths. Preserves incoming request-id / x-request-id if present.
func writeJSON(w http.ResponseWriter, r *http.Request, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("anthropic-organization-id", fakeids.OrgID)
	w.Header().Set("anthropic-workspace-id", fakeids.WorkspaceID)
	w.Header().Set("anthropic-version", "2023-06-01")
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
	w.Header().Set("request-id", rid)
	w.Header().Set("x-request-id", rid)
	_, _ = w.Write([]byte(body))
}

func handleProfile(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"account":{"uuid":"`+fakeids.AccountID+`","email":"user@claudgravity.local","email_address":"user@claudgravity.local"},"organization":{"uuid":"`+fakeids.OrgID+`","name":"ClaudGravity Workspace","organization_type":"claude_enterprise","rate_limit_tier":"tier_4"}}`)
}

func handleValidate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"valid":true,"account_uuid":"`+fakeids.AccountID+`","organization_uuid":"`+fakeids.OrgID+`"}`)
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"metrics_logging_enabled":false}`)
}

func handleSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"uuid":"`+fakeids.AccountID+`","checksum":"none","settings":{}}`)
}

func handleTelemetry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{}`)
}

func handleOrganizationsCurrent(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"uuid":"`+fakeids.OrgID+`","name":"ClaudGravity Workspace","organization_type":"claude_enterprise","rate_limit_tier":"default_claude_max_20x","seat_tier":"enterprise","subscription_type":"enterprise","has_extra_usage_enabled":true,"billing_type":"stripe","has_accepted_latest_terms":true,"capabilities":["claude_code","tools","thinking","prompt_caching","model_selector"],"feature_flags":{"claude_code":true,"web_search":true,"code_execution":true}}`)
}

func handleOrganizationsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"data":[{"uuid":"`+fakeids.OrgID+`","name":"ClaudGravity Workspace","organization_type":"claude_enterprise","rate_limit_tier":"tier_4"}],"has_more":false,"first_id":"`+fakeids.OrgID+`","last_id":"`+fakeids.OrgID+`"}`)
}

// handleAuthCurrentUser answers GET /api/auth/current_user — the
// endpoint CLI calls before anything else to confirm the session is
// authenticated; it must include "memberships" (not just
// "organizations"), both with a nested "organization" object, or a
// strict client's own org-membership check sees an empty/missing list.
func handleAuthCurrentUser(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"uuid":"`+fakeids.AccountID+`","email":"user@claudgravity.local","email_address":"user@claudgravity.local","name":"ClaudGravity User","full_name":"ClaudGravity User","has_accepted_latest_terms":true,"has_completed_onboarding":true,"is_staff":false,"organizations":[{"uuid":"`+fakeids.OrgID+`","name":"ClaudGravity Workspace","organization_type":"claude_enterprise","rate_limit_tier":"tier_4","has_accepted_latest_terms":true}],"memberships":[{"uuid":"`+fakeids.OrgID+`-member","organization_uuid":"`+fakeids.OrgID+`","organization":{"uuid":"`+fakeids.OrgID+`","name":"ClaudGravity Workspace","organization_type":"claude_enterprise","rate_limit_tier":"tier_4","has_accepted_latest_terms":true},"role":"admin"}]}`)
}

func handleBilling(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"tier":"tier_4","status":"active","has_payment_method":true,"currency":"usd"}`)
}

func handlePolicyLimits(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"restrictions":{},"compliance_taints":[]}`)
}

func handleFirstTokenDate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"first_token_date":null}`)
}

func handleNotificationPreferences(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"account_id":"`+fakeids.AccountID+`","organization_id":"`+fakeids.OrgID+`","preferences":{"feature_preference":{"bogosort":{"enable_push":true}}},"push_reachability":{"has_active_channel":true,"platforms":["cli"]}}`)
}

func handleOAuthAccountSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{}`)
}

func handleGroveNoticeViewed(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"viewed":true}`)
}

func handleAccount(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"account":{"uuid":"`+fakeids.AccountID+`","email":"user@claudgravity.local","email_address":"user@claudgravity.local"},"organization":{"uuid":"`+fakeids.OrgID+`","name":"ClaudGravity Workspace","organization_type":"claude_enterprise","rate_limit_tier":"tier_4"}}`)
}

func handleTeams(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"data":[{"uuid":"`+fakeids.OrgID+`","name":"ClaudGravity Workspace"}],"has_more":false}`)
}

// handleCacheTouch answers POST /v1/messages/cache_touch — a lightweight
// ping Claude Code uses to extend the Anthropic prompt-cache ephemeral
// TTL without a real generation. See the package doc comment: this is
// the specific endpoint whose absence degrades the rest of the session,
// not just one request.
func handleCacheTouch(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"status":"ok","touched":true}`)
}

func handleModelSelector(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{}`)
}

func handleModelSelectorState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"model_selector_state":[]}`)
}

func handleOnboarding(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"guides":[],"has_completed_onboarding":true}`)
}

func handleResetRateLimits(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"status":"ok"}`)
}

func handleFeedback(w http.ResponseWriter, r *http.Request) {
	n := atomic.AddUint64(&requestIDCounter, 1)
	h := sha256.Sum256([]byte(fmt.Sprintf("feedback-%d-%d", time.Now().UnixNano(), n)))
	writeJSON(w, r, fmt.Sprintf(`{"feedback_id":"fb_%s"}`, hex.EncodeToString(h[:12])))
}

func handleEval(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"status":200,"features":{}}`)
}

func handleSessions(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, r, `{"data":[],"has_more":false}`)
}

// handleUsage answers GET /api/oauth/usage — GAP-CS-01: provides full
// usage structures expected by Claude Code CLI v2.1+ (/usage command).
func handleUsage(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	resets5h := now.Add(5 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	resets7d := now.Add(7 * 24 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")

	fiveHourWindow := map[string]interface{}{
		"utilization":       0.05,
		"resets_at":         resets5h,
		"limit_dollars":     nil,
		"used_dollars":      nil,
		"remaining_dollars": nil,
		"locked_reason":     nil,
	}
	weeklyWindow := map[string]interface{}{
		"utilization":       0.05,
		"resets_at":         resets7d,
		"limit_dollars":     nil,
		"used_dollars":      nil,
		"remaining_dollars": nil,
		"locked_reason":     nil,
	}

	limits := []map[string]interface{}{
		{
			"kind":      "session",
			"group":     "session",
			"percent":   0.05,
			"severity":  "normal",
			"resets_at": resets5h,
			"scope":     nil,
			"is_active": true,
		},
		{
			"kind":      "weekly_all",
			"group":     "weekly",
			"percent":   0.05,
			"severity":  "normal",
			"resets_at": resets7d,
			"scope":     nil,
			"is_active": false,
		},
		{
			"kind":      "weekly_scoped",
			"group":     "weekly",
			"percent":   0.05,
			"severity":  "normal",
			"resets_at": resets7d,
			"scope": map[string]interface{}{
				"model": map[string]interface{}{
					"display_name": "Opus",
				},
			},
			"is_active": false,
		},
		{
			"kind":      "weekly_scoped",
			"group":     "weekly",
			"percent":   0.05,
			"severity":  "normal",
			"resets_at": resets7d,
			"scope": map[string]interface{}{
				"model": map[string]interface{}{
					"display_name": "Sonnet",
				},
			},
			"is_active": false,
		},
	}

	resp := map[string]interface{}{
		"five_hour":            fiveHourWindow,
		"seven_day":            weeklyWindow,
		"seven_day_oauth_apps": nil,
		"seven_day_opus":       weeklyWindow,
		"seven_day_sonnet":     weeklyWindow,
		"extra_usage": map[string]interface{}{
			"is_enabled":           false,
			"monthly_limit":        0,
			"used_credits":         0,
			"utilization":          0,
			"currency":             "USD",
			"decimal_places":       nil,
			"disabled_reason":      nil,
			"user_disabled":        false,
			"spend_limit_reached":  false,
			"credits_ever_enabled": false,
			"daily":                nil,
			"weekly":               nil,
		},
		"limits": limits,
		"spend": map[string]interface{}{
			"used":                 map[string]interface{}{"amount_minor": 0, "currency": "USD", "exponent": 2},
			"limit":                nil,
			"percent":              0,
			"severity":             "normal",
			"enabled":              false,
			"disabled_reason":      nil,
			"cap":                  nil,
			"balance":              nil,
			"auto_reload":          nil,
			"can_purchase_credits": false,
			"can_toggle":           false,
		},
	}

	ratelimit.ApplyHeaders(w.Header(), nil, "claude-opus-5-5")
	b, _ := json.Marshal(resp)
	writeJSON(w, r, string(b))
}

// RegisterHandlers registers every CLI control-endpoint stub this
// package owns onto mux. Call it before any catch-all ("/") handler is
// registered on the same mux — Go's http.ServeMux always prefers the
// more specific literal pattern over a catch-all regardless of
// registration order, so exact collisions aren't a risk, but registering
// first keeps the intent legible.
// Optional middlewares are applied in order to each registered handler.
func RegisterHandlers(mux *http.ServeMux, middlewares ...func(http.Handler) http.Handler) {
	wrap := func(h http.Handler) http.Handler {
		for i := len(middlewares) - 1; i >= 0; i-- {
			h = middlewares[i](h)
		}
		return h
	}
	handle := func(pattern string, handler http.HandlerFunc) {
		mux.Handle(pattern, wrap(handler))
	}

	handle("/api/claude_cli_profile", handleProfile)
	handle("/api/oauth/profile", handleProfile)
	handle("/api/oauth/validate", handleValidate)
	handle("/api/claude_code/organizations/metrics_enabled", handleMetrics)
	handle("/api/claude_code/organizations/{id}/metrics_enabled", handleMetrics)
	handle("/api/claude_code/settings", handleSettings)
	handle("/managed/settings", handleSettings)
	handle("/api/event_logging/", handleTelemetry)
	handle("/api/event_logging", handleTelemetry)
	handle("/api/event_logging/v2/batch", handleTelemetry)
	handle("/api/event_logging/v2/batch/", handleTelemetry)
	handle("/api/telemetry", handleTelemetry)
	handle("/api/telemetry/", handleTelemetry)
	handle("/api/claude_code/telemetry", handleTelemetry)
	handle("/api/claude_code/telemetry/", handleTelemetry)
	handle("/api/claude_code/crash_reports", handleTelemetry)
	handle("/api/claude_code/crash_reports/", handleTelemetry)
	handle("/api/organizations", handleOrganizationsList)
	handle("/api/organizations/", handleOrganizationsList)
	handle("/api/organizations/current", handleOrganizationsCurrent)
	handle("/api/auth/current_user", handleAuthCurrentUser)
	handle("/api/organizations/current/billing", handleBilling)
	handle("/api/organizations/{id}/billing", handleBilling)
	handle("/api/organizations/{id}", handleOrganizationsCurrent)
	handle("/api/claude_code/policy_limits", handlePolicyLimits)
	handle("/api/organization/claude_code_first_token_date", handleFirstTokenDate)
	handle("/api/organizations/claude_code_first_token_date", handleFirstTokenDate)
	handle("/api/organizations/{id}/claude_code_first_token_date", handleFirstTokenDate)
	handle("/api/claude_code/notification/preferences", handleNotificationPreferences)
	handle("/api/account/settings", handleOAuthAccountSettings)
	handle("/api/account/grove_notice_viewed", handleGroveNoticeViewed)
	handle("/api/account", handleAccount)
	handle("/api/account/", handleAccount)
	handle("/api/user", handleAuthCurrentUser)
	handle("/api/user/", handleAuthCurrentUser)
	handle("/api/users/current", handleAuthCurrentUser)
	handle("/api/eval/", handleEval)
	handle("/api/eval", handleEval)
	handle("/api/eval-authed/", handleEval)
	handle("/api/eval-authed", handleEval)
	handle("/api/claude_cli_feedback", handleFeedback)
	handle("/api/feedback", handleFeedback)
	handle("/api/feedback/", handleFeedback)
	handle("/api/teams", handleTeams)
	handle("/api/teams/", handleTeams)
	handle("/api/oauth/account/settings", handleOAuthAccountSettings)
	handle("/api/oauth/account/grove_notice_viewed", handleGroveNoticeViewed)
	handle("/v1/messages/cache_touch", handleCacheTouch)
	handle("/v1/messages/cache_touch/", handleCacheTouch)
	handle("/v1/code/sessions", handleSessions)
	handle("/v1/code/sessions/", handleSessions)
	handle("/v1/sessions", handleSessions)
	handle("/v1/sessions/", handleSessions)
	handle("/api/organizations/{id}/model_selector/{surface}", handleModelSelector)
	handle("/api/organizations/{id}/model_selector", handleModelSelector)
	handle("/api/organizations/{id}/model_selector_state", handleModelSelectorState)
	handle("/api/organizations/{id}/model_selector_state/{surface}", handleModelSelectorState)
	handle("/api/organizations/{id}/claude_code/onboarding", handleOnboarding)
	handle("/api/organizations/{id}/claude_code/onboarding/", handleOnboarding)
	handle("/api/organizations/{id}/reset_rate_limits", handleResetRateLimits)
	handle("/api/oauth/usage", handleUsage)
	// GAP-CS-02: neither path was registered anywhere — CLI's own
	// model-access permission check 404'd. mitm.HandleBootstrapSpoof
	// already exists (ported for the MITM-intercepted path) but was
	// never wired onto the plain top-level mux these other CLI stubs
	// live on.
	handle("/api/claude_cli/bootstrap", mitm.HandleBootstrapSpoof)
	handle("/api/claude_code/bootstrap", mitm.HandleBootstrapSpoof)
}
