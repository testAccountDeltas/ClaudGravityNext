package mitm

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claudgravity/internal/fakeids"
)

func TestHandleBootstrapSpoof_HeadersAndDefaults(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/claude_cli/bootstrap", nil)
	rec := httptest.NewRecorder()

	HandleBootstrapSpoof(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if org := rec.Header().Get("anthropic-organization-id"); org != fakeids.OrgID {
		t.Errorf("anthropic-organization-id = %q, want %q", org, fakeids.OrgID)
	}
	if ws := rec.Header().Get("anthropic-workspace-id"); ws != fakeids.WorkspaceID {
		t.Errorf("anthropic-workspace-id = %q, want %q", ws, fakeids.WorkspaceID)
	}
	if ver := rec.Header().Get("anthropic-version"); ver != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", ver)
	}
	rid := rec.Header().Get("request-id")
	if rid == "" || !strings.HasPrefix(rid, "req_") {
		t.Errorf("request-id = %q, want req_<hex>", rid)
	}
	if xrid := rec.Header().Get("x-request-id"); xrid != rid {
		t.Errorf("x-request-id = %q, want %q", xrid, rid)
	}
}

func TestHandleBootstrapSpoof_PreservesIncomingRequestID(t *testing.T) {
	// From x-request-id header on request
	req1 := httptest.NewRequest(http.MethodGet, "/api/claude_cli/bootstrap", nil)
	req1.Header.Set("x-request-id", "test-trace-abc-123")
	rec1 := httptest.NewRecorder()
	HandleBootstrapSpoof(rec1, req1)
	if rid := rec1.Header().Get("request-id"); rid != "test-trace-abc-123" {
		t.Errorf("request-id = %q, want test-trace-abc-123", rid)
	}
	if xrid := rec1.Header().Get("x-request-id"); xrid != "test-trace-abc-123" {
		t.Errorf("x-request-id = %q, want test-trace-abc-123", xrid)
	}

	// From request-id header on request
	req2 := httptest.NewRequest(http.MethodGet, "/api/claude_cli/bootstrap", nil)
	req2.Header.Set("request-id", "test-req-xyz-456")
	rec2 := httptest.NewRecorder()
	HandleBootstrapSpoof(rec2, req2)
	if rid := rec2.Header().Get("request-id"); rid != "test-req-xyz-456" {
		t.Errorf("request-id = %q, want test-req-xyz-456", rid)
	}

	// From existing header on ResponseWriter (middleware)
	req3 := httptest.NewRequest(http.MethodGet, "/api/claude_cli/bootstrap", nil)
	rec3 := httptest.NewRecorder()
	rec3.Header().Set("request-id", "middleware-trace-789")
	HandleBootstrapSpoof(rec3, req3)
	if rid := rec3.Header().Get("request-id"); rid != "middleware-trace-789" {
		t.Errorf("request-id = %q, want middleware-trace-789", rid)
	}
}

func TestHandleBootstrapSpoof_ModelEntitlements(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/claude_cli/bootstrap", nil)
	rec := httptest.NewRecorder()

	HandleBootstrapSpoof(rec, req)

	var payload struct {
		ModelAccess []struct {
			APIName  string `json:"api_name"`
			Entitled bool   `json:"entitled"`
		} `json:"model_access"`
	}

	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode bootstrap json: %v", err)
	}

	accessMap := make(map[string]bool)
	for _, m := range payload.ModelAccess {
		if !m.Entitled {
			t.Errorf("model %q has entitled=false, expected true", m.APIName)
		}
		accessMap[m.APIName] = m.Entitled
	}

	requiredModels := []string{
		// Static Claude models and aliases
		"opus", "sonnet", "haiku", "fable", "mythos",
		"claude-opus-5-5", "claude-opus-5", "claude-sonnet-5", "claude-fable-5", "claude-mythos-5",
		"claude-3-7-sonnet", "claude-3-5-sonnet", "claude-3-5-haiku",
		"claude-3-haiku-20240307", "claude-3-sonnet-20240229",
		"claude-opus-4-0", "claude-opus-4-5", "claude-opus-4-6",
		// Gemini models
		"gemini-2.5-pro", "gemini-2.5-flash", "gemini-3.8-flash-tiered",
	}

	for _, model := range requiredModels {
		entitled, exists := accessMap[model]
		if !exists {
			t.Errorf("required model %q missing from model_access", model)
		} else if !entitled {
			t.Errorf("model %q has entitled=false", model)
		}
	}
}

type testRoundTripper func(*http.Request) (*http.Response, error)

func (f testRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestPassthroughToReal_RewritesHostAndStripsHopByHop(t *testing.T) {
	var capturedHost string
	var capturedHeaders http.Header

	origClient := passthroughClient
	defer func() { passthroughClient = origClient }()

	passthroughClient = &http.Client{
		Transport: testRoundTripper(func(req *http.Request) (*http.Response, error) {
			capturedHost = req.Host
			capturedHeaders = req.Header.Clone()
			return &http.Response{
				StatusCode: 200,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
			}, nil
		}),
	}

	req := httptest.NewRequest(http.MethodGet, "http://localhost:8901/api/unmapped_endpoint", nil)
	req.Host = "localhost:8901"
	req.Header.Set("Proxy-Authorization", "Bearer secret123")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Custom-Header", "hello")

	rec := httptest.NewRecorder()
	PassthroughToReal(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if capturedHost != "api.anthropic.com" {
		t.Errorf("capturedHost = %q, want api.anthropic.com", capturedHost)
	}
	if v := capturedHeaders.Get("Proxy-Authorization"); v != "" {
		t.Errorf("Proxy-Authorization was not stripped: %q", v)
	}
	if v := capturedHeaders.Get("Connection"); v != "" {
		t.Errorf("Connection was not stripped: %q", v)
	}
	if v := capturedHeaders.Get("Custom-Header"); v != "hello" {
		t.Errorf("Custom-Header = %q, want hello", v)
	}
}
