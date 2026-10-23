// Characterization tests for the ui.go port (register.go + auth.go +
// accounts_api.go/logs_api.go/keys_api.go/oauth_api.go/bootstrap_api.go/
// page.go) — proves RegisterUIHandlers wires up every route v1's ui.go
// did, requireDashboardAuth gates them the same way, and the handlers
// that don't need a live Google account produce the same JSON shapes.
package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claudgravity/internal/config"
	"claudgravity/internal/requestlog"
)

func testConfigManager(t *testing.T) *config.ConfigManager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	cm, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	return cm
}

// --- requireDashboardAuth --------------------------------------------------

func TestRequireDashboardAuth_RejectsMissingAndWrongCredentials(t *testing.T) {
	cm := testConfigManager(t)
	called := false
	h := requireDashboardAuth(cm, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("missing credentials: got %d (called=%v), want 401", rec.Code, called)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.SetBasicAuth("admin", "definitely-not-the-password")
	rec2 := httptest.NewRecorder()
	h(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: got %d, want 401", rec2.Code)
	}
}

func TestRequireDashboardAuth_AcceptsCorrectCredentials(t *testing.T) {
	cm := testConfigManager(t)
	called := false
	h := requireDashboardAuth(cm, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", cm.DashboardPassword())
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("correct password: got %d (called=%v), want 200", rec.Code, called)
	}
}

func TestRequireDashboardAuth_EmptyOrWhitespacePasswordFailsClosed(t *testing.T) {
	// 1. nil ConfigManager
	called := false
	hNil := requireDashboardAuth(nil, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	hNil(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized || called {
		t.Fatalf("nil cm: got %d (called=%v), want 401", rec.Code, called)
	}

	// 2. ConfigManager with whitespace password
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"server":{"dashboard_password":"   "}}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cmWs, err := config.LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	called = false
	hWs := requireDashboardAuth(cmWs, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	rec2 := httptest.NewRecorder()
	hWs(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec2.Code != http.StatusUnauthorized || called {
		t.Fatalf("whitespace password: got %d (called=%v), want 401", rec2.Code, called)
	}
}

// --- RegisterUIHandlers route registration ---------------------------------

func TestRegisterUIHandlers_RegistersEveryRouteBehindAuth(t *testing.T) {
	cm := testConfigManager(t)
	mux := http.NewServeMux()
	RegisterUIHandlers(mux, cm)

	// Every one of these must exist (not fall through to mux's default
	// 404) and must demand auth (401 without credentials) — mirrors v1's
	// ui.go RegisterUIHandlers route list exactly, except /api/bootstrap.
	authedRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/"},
		{http.MethodGet, "/dashboard"},
		{http.MethodGet, "/dashboard/"},
		{http.MethodGet, "/api/accounts"},
		{http.MethodPost, "/api/accounts/toggle?email=x@example.com&disabled=true"},
		{http.MethodGet, "/api/refresh-quotas"},
		{http.MethodGet, "/api/warmup?email=x@example.com"},
		{http.MethodGet, "/api/warmup-all"},
		{http.MethodGet, "/api/logs"},
		{http.MethodGet, "/api/sessions"},
		{http.MethodGet, "/api/session?id=abc"},
		{http.MethodGet, "/api/model-catalog"},
		{http.MethodGet, "/api/keys"},
		{http.MethodGet, "/oauth/login"},
		{http.MethodGet, "/oauth/callback"},
	}
	for _, rt := range authedRoutes {
		req := httptest.NewRequest(rt.method, rt.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: got %d without credentials, want 401 (route missing or not gated)", rt.method, rt.path, rec.Code)
		}
	}
}

func TestRegisterUIHandlers_BootstrapIsNotBehindDashboardAuth(t *testing.T) {
	cm := testConfigManager(t)
	mux := http.NewServeMux()
	RegisterUIHandlers(mux, cm)

	// No Basic Auth header at all — if /api/bootstrap were behind
	// requireDashboardAuth, this would be 401. It must instead reach
	// handleAPIBootstrap's own tenant-key check and fail THAT way (401
	// with a different, key-specific error body), proving the two gates
	// are distinct, matching v1's explicit carve-out.
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401 (missing tenant key)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing ?key=") {
		t.Fatalf("expected tenant-key error body, got: %s", rec.Body.String())
	}
}

// --- handleDashboard --------------------------------------------------------

func TestHandleDashboard_ServesEmbeddedHTML(t *testing.T) {
	for _, p := range []string{"/", "/dashboard", "/dashboard/"} {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		rec := httptest.NewRecorder()
		handleDashboard(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: got %d, want 200", p, rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.Contains(ct, "text/html") {
			t.Fatalf("%s: Content-Type = %q, want text/html", p, ct)
		}
		if !strings.Contains(rec.Body.String(), "<!DOCTYPE html>") {
			t.Fatalf("%s: embedded dashboard.html doesn't look like HTML", p)
		}
	}
}

func TestHandleDashboard_404sOnOtherPaths(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rec := httptest.NewRecorder()
	handleDashboard(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

// --- handleAPIKeys -----------------------------------------------------------

func TestHandleAPIKeys_CreateListDeleteRoundTrip(t *testing.T) {
	cm := testConfigManager(t)

	// Create.
	body := strings.NewReader(`{"name":"test-client","strategy":"sticky","model":"gemini-2.5-flash"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/keys", body)
	rec := httptest.NewRecorder()
	handleAPIKeys(cm, rec, req)
	if rec.Code != http.StatusOK && rec.Code != 0 {
		t.Fatalf("create: got %d, want 200/zero-default", rec.Code)
	}
	var created struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create response not JSON: %v (%s)", err, rec.Body.String())
	}
	if created.Key == "" || created.Name != "test-client" {
		t.Fatalf("unexpected create response: %+v", created)
	}

	// List.
	req2 := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	rec2 := httptest.NewRecorder()
	handleAPIKeys(cm, rec2, req2)
	var tenants map[string]config.TenantConfig
	if err := json.Unmarshal(rec2.Body.Bytes(), &tenants); err != nil {
		t.Fatalf("list response not JSON: %v", err)
	}

	// Plaintext key must NOT be leaked
	if _, ok := tenants[created.Key]; ok {
		t.Fatalf("raw key %q must NOT be present in /api/keys response", created.Key)
	}

	// Masked key MUST be present
	maskedKey := requestlog.MaskKey(created.Key)
	if _, ok := tenants[maskedKey]; !ok {
		t.Fatalf("masked key %q missing from list: %+v", maskedKey, tenants)
	}

	// Delete by masked key.
	req3 := httptest.NewRequest(http.MethodDelete, "/api/keys?key="+maskedKey, nil)
	rec3 := httptest.NewRecorder()
	handleAPIKeys(cm, rec3, req3)
	if rec3.Code != http.StatusOK && rec3.Code != 0 {
		t.Fatalf("delete: got %d", rec3.Code)
	}

	req4 := httptest.NewRequest(http.MethodGet, "/api/keys", nil)
	rec4 := httptest.NewRecorder()
	handleAPIKeys(cm, rec4, req4)
	var tenantsAfter map[string]config.TenantConfig
	_ = json.Unmarshal(rec4.Body.Bytes(), &tenantsAfter)
	if _, ok := tenantsAfter[maskedKey]; ok {
		t.Fatalf("key %q still present after delete", maskedKey)
	}
}

// --- handleAPIBootstrap -------------------------------------------------------

func TestHandleAPIBootstrap_RequiresValidTenantKey(t *testing.T) {
	cm := testConfigManager(t)

	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	rec := httptest.NewRecorder()
	handleAPIBootstrap(cm, rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no key: got %d, want 401", rec.Code)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/bootstrap?key=not-a-real-key", nil)
	rec2 := httptest.NewRecorder()
	handleAPIBootstrap(cm, rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("bad key: got %d, want 401", rec2.Code)
	}
}

func TestHandleAPIBootstrap_ValidKeyReturnsServerConfig(t *testing.T) {
	cm := testConfigManager(t)
	key, err := cm.CreateAPIKey("bootstrap-test", "", "", "")
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap?key="+key, nil)
	rec := httptest.NewRecorder()
	handleAPIBootstrap(cm, rec, req)
	if rec.Code != http.StatusOK && rec.Code != 0 {
		t.Fatalf("got %d, want 200", rec.Code)
	}

	var resp struct {
		ProxySecret string `json:"proxy_secret"`
		TLSPort     int    `json:"tls_port"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response not JSON: %v (%s)", err, rec.Body.String())
	}
	sc := cm.GetServerConfig()
	if resp.ProxySecret != sc.ProxySecret {
		t.Fatalf("proxy_secret mismatch: got %q, want %q", resp.ProxySecret, sc.ProxySecret)
	}
	if resp.TLSPort != sc.TLSPort {
		t.Fatalf("tls_port mismatch: got %d, want %d", resp.TLSPort, sc.TLSPort)
	}
}

// Authorization: Bearer header must work as an alternative to ?key=,
// exactly like v1.
func TestHandleAPIBootstrap_AcceptsBearerHeader(t *testing.T) {
	cm := testConfigManager(t)
	key, err := cm.CreateAPIKey("bootstrap-bearer-test", "", "", "")
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	handleAPIBootstrap(cm, rec, req)
	if rec.Code != http.StatusOK && rec.Code != 0 {
		t.Fatalf("got %d, want 200", rec.Code)
	}
}

// --- handleAPIModelCatalog ----------------------------------------------------

func TestHandleAPIModelCatalog_ReturnsJSONArray(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/model-catalog", nil)
	rec := httptest.NewRecorder()
	handleAPIModelCatalog(rec, req)
	if rec.Code != http.StatusOK && rec.Code != 0 {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	var out []interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response not a JSON array: %v (%s)", err, rec.Body.String())
	}
}

// --- handleAPIAccounts: bad-input shape (no live account pool needed) -------

func TestHandleAPIAccounts_DeleteRequiresEmail(t *testing.T) {
	rec := httptest.NewRecorder()
	handleAPIAccounts(rec, httptest.NewRequest(http.MethodDelete, "/api/accounts", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestHandleAPIAccounts_DeleteUnknownEmailIs404(t *testing.T) {
	rec := httptest.NewRecorder()
	handleAPIAccounts(rec, httptest.NewRequest(http.MethodDelete, "/api/accounts?email=nobody@example.com", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestHandleAPIAccountToggle_RejectsNonPOST(t *testing.T) {
	rec := httptest.NewRecorder()
	handleAPIAccountToggle(rec, httptest.NewRequest(http.MethodGet, "/api/accounts/toggle?email=x@example.com&disabled=true", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got %d, want 405", rec.Code)
	}
}

func TestHandleAPIAccountToggle_RequiresEmailAndDisabled(t *testing.T) {
	rec := httptest.NewRecorder()
	handleAPIAccountToggle(rec, httptest.NewRequest(http.MethodPost, "/api/accounts/toggle", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

// --- handleAPISessionDetail / handleAPILogs: shape-only, no DB required ----

func TestHandleAPISessionDetail_RequiresID(t *testing.T) {
	rec := httptest.NewRecorder()
	handleAPISessionDetail(rec, httptest.NewRequest(http.MethodGet, "/api/session", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestHandleAPISessionDetail_UnknownSessionIs404(t *testing.T) {
	rec := httptest.NewRecorder()
	handleAPISessionDetail(rec, httptest.NewRequest(http.MethodGet, "/api/session?id=nonexistent-session", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}
