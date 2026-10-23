package clistubs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GAP-P0-02: each of these paths previously fell through to a 401
// (dashboard's Basic Auth catch-all "/") or a 404 (httpapi's own /v1/
// catch-all, for cache_touch specifically) — confirm every registered
// stub now answers 200 with a JSON body instead.
func TestRegisterHandlers_AllPathsRespond200JSON(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHandlers(mux)

	paths := []struct {
		method string
		path   string
	}{
		{"GET", "/api/auth/current_user"},
		{"HEAD", "/api/hello"},
		{"GET", "/api/hello"},
		{"GET", "/v1/oauth/hello"},
		{"GET", "/api/web/domain_info?domain=example.com"},
		{"GET", "/api/oauth/claude_cli/roles"},
		{"POST", "/api/oauth/claude_cli/create_api_key"},
		{"GET", "/api/claude_code/user_settings"},
		{"GET", "/api/claude_code/team_memory"},
		{"POST", "/v1/messages/cache_touch"},
		{"GET", "/api/claude_code/settings"},
		{"GET", "/api/claude_code/policy_limits"},
		{"POST", "/api/claude_code/telemetry"},
		{"POST", "/api/event_logging/v2/batch"},
		{"GET", "/api/organizations/current"},
		{"GET", "/api/organizations/abc-123"},
		{"GET", "/api/organizations/abc-123/billing"},
		{"GET", "/api/organizations/abc-123/model_selector_state"},
		{"GET", "/api/organizations/abc-123/claude_code/onboarding"},
		{"GET", "/api/account"},
		{"GET", "/api/teams"},
		{"POST", "/api/claude_cli_feedback"},
		{"POST", "/api/eval"},
		{"POST", "/api/eval/"},
		{"POST", "/api/eval-authed"},
		{"POST", "/api/eval-authed/"},
		{"GET", "/v1/code/sessions"},
		{"GET", "/v1/sessions"},
	}
	for _, p := range paths {
		req := httptest.NewRequest(p.method, p.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s %s: status = %d, want 200", p.method, p.path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s %s: Content-Type = %q, want application/json", p.method, p.path, ct)
		}
	}
}

func TestHandleAuthCurrentUser_CLI21Parity(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/auth/current_user", nil)
	rec := httptest.NewRecorder()

	handleAuthCurrentUser(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var user struct {
		Name                   string `json:"name"`
		FullName               string `json:"full_name"`
		Email                  string `json:"email"`
		EmailAddress           string `json:"email_address"`
		HasAcceptedLatestTerms bool   `json:"has_accepted_latest_terms"`
		HasCompletedOnboarding bool   `json:"has_completed_onboarding"`
		IsStaff                bool   `json:"is_staff"`
		Organizations          []struct {
			OrganizationType       string `json:"organization_type"`
			RateLimitTier          string `json:"rate_limit_tier"`
			HasAcceptedLatestTerms bool   `json:"has_accepted_latest_terms"`
		} `json:"organizations"`
		Memberships []struct {
			Organization struct {
				OrganizationType       string `json:"organization_type"`
				RateLimitTier          string `json:"rate_limit_tier"`
				HasAcceptedLatestTerms bool   `json:"has_accepted_latest_terms"`
			} `json:"organization"`
		} `json:"memberships"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &user); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}

	if user.Name != "ClaudGravity User" {
		t.Errorf("name = %q, want 'ClaudGravity User'", user.Name)
	}
	if user.FullName != "ClaudGravity User" {
		t.Errorf("full_name = %q, want 'ClaudGravity User'", user.FullName)
	}
	if user.EmailAddress != "user@claudgravity.local" {
		t.Errorf("email_address = %q, want 'user@claudgravity.local'", user.EmailAddress)
	}
	if !user.HasAcceptedLatestTerms {
		t.Error("has_accepted_latest_terms = false, want true")
	}
	if !user.HasCompletedOnboarding {
		t.Error("has_completed_onboarding = false, want true")
	}
	if user.IsStaff {
		t.Error("is_staff = true, want false")
	}

	if len(user.Organizations) == 0 {
		t.Fatal("organizations array is empty")
	}
	if org := user.Organizations[0]; org.OrganizationType != "claude_enterprise" || org.RateLimitTier != "tier_4" || !org.HasAcceptedLatestTerms {
		t.Errorf("organizations[0] = %+v, want claude_enterprise/tier_4/accepted", org)
	}

	if len(user.Memberships) == 0 {
		t.Fatal("memberships array is empty")
	}
	if mOrg := user.Memberships[0].Organization; mOrg.OrganizationType != "claude_enterprise" || mOrg.RateLimitTier != "tier_4" || !mOrg.HasAcceptedLatestTerms {
		t.Errorf("memberships[0].organization = %+v, want claude_enterprise/tier_4/accepted", mOrg)
	}
}

func TestHandleOrganizationsCurrent_CLI21Parity(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/organizations/current", nil)
	rec := httptest.NewRecorder()

	handleOrganizationsCurrent(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var org struct {
		OrganizationType       string          `json:"organization_type"`
		RateLimitTier          string          `json:"rate_limit_tier"`
		SeatTier               string          `json:"seat_tier"`
		SubscriptionType       string          `json:"subscription_type"`
		HasExtraUsageEnabled   bool            `json:"has_extra_usage_enabled"`
		BillingType            string          `json:"billing_type"`
		HasAcceptedLatestTerms bool            `json:"has_accepted_latest_terms"`
		Capabilities           []string        `json:"capabilities"`
		FeatureFlags           map[string]bool `json:"feature_flags"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &org); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}

	if org.OrganizationType != "claude_enterprise" {
		t.Errorf("organization_type = %q, want 'claude_enterprise'", org.OrganizationType)
	}
	if org.RateLimitTier != "default_claude_max_20x" {
		t.Errorf("rate_limit_tier = %q, want 'default_claude_max_20x'", org.RateLimitTier)
	}
	if org.SeatTier != "enterprise" {
		t.Errorf("seat_tier = %q, want 'enterprise'", org.SeatTier)
	}
	if org.SubscriptionType != "enterprise" {
		t.Errorf("subscription_type = %q, want 'enterprise'", org.SubscriptionType)
	}
	if !org.HasExtraUsageEnabled {
		t.Error("has_extra_usage_enabled = false, want true")
	}
	if org.BillingType != "stripe" {
		t.Errorf("billing_type = %q, want 'stripe'", org.BillingType)
	}
	if !org.HasAcceptedLatestTerms {
		t.Error("has_accepted_latest_terms = false, want true")
	}

	capMap := make(map[string]bool)
	for _, c := range org.Capabilities {
		capMap[c] = true
	}
	for _, expectedCap := range []string{"claude_code", "tools", "thinking", "prompt_caching", "model_selector"} {
		if !capMap[expectedCap] {
			t.Errorf("missing capability %q", expectedCap)
		}
	}

	if !org.FeatureFlags["claude_code"] || !org.FeatureFlags["web_search"] || !org.FeatureFlags["code_execution"] {
		t.Errorf("feature_flags = %+v, want claude_code, web_search, code_execution all true", org.FeatureFlags)
	}
}

func TestHandleProfile_CLI21Parity(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/claude_cli_profile", nil)
	rec := httptest.NewRecorder()

	handleProfile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var prof struct {
		Organization struct {
			OrganizationType string `json:"organization_type"`
			RateLimitTier    string `json:"rate_limit_tier"`
		} `json:"organization"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &prof); err != nil {
		t.Fatalf("failed to decode json: %v", err)
	}

	if prof.Organization.OrganizationType != "claude_enterprise" {
		t.Errorf("organization.organization_type = %q, want 'claude_enterprise'", prof.Organization.OrganizationType)
	}
	if prof.Organization.RateLimitTier != "tier_4" {
		t.Errorf("organization.rate_limit_tier = %q, want 'tier_4'", prof.Organization.RateLimitTier)
	}
}

func TestWriteJSON_RequestIDPreservation(t *testing.T) {
	// 1. Preserves incoming x-request-id
	req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req1.Header.Set("x-request-id", "in-trace-123")
	rec1 := httptest.NewRecorder()
	writeJSON(rec1, req1, `{}`)
	if got := rec1.Header().Get("request-id"); got != "in-trace-123" {
		t.Errorf("request-id = %q, want in-trace-123", got)
	}
	if got := rec1.Header().Get("x-request-id"); got != "in-trace-123" {
		t.Errorf("x-request-id = %q, want in-trace-123", got)
	}

	// 2. Preserves incoming request-id
	req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
	req2.Header.Set("request-id", "in-rid-456")
	rec2 := httptest.NewRecorder()
	writeJSON(rec2, req2, `{}`)
	if got := rec2.Header().Get("request-id"); got != "in-rid-456" {
		t.Errorf("request-id = %q, want in-rid-456", got)
	}

	// 3. Preserves existing header from middleware
	req3 := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec3 := httptest.NewRecorder()
	rec3.Header().Set("request-id", "middleware-rid-789")
	writeJSON(rec3, req3, `{}`)
	if got := rec3.Header().Get("request-id"); got != "middleware-rid-789" {
		t.Errorf("request-id = %q, want middleware-rid-789", got)
	}

	// 4. Mints new ID when none incoming
	req4 := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec4 := httptest.NewRecorder()
	writeJSON(rec4, req4, `{}`)
	rid := rec4.Header().Get("request-id")
	if rid == "" || !strings.HasPrefix(rid, "req_") {
		t.Errorf("minted request-id = %q, want req_<hex>", rid)
	}
}
