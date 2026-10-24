package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"claudgravity/internal/accounts"
	"claudgravity/internal/requestlog"
)

func secEnv(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	cm := testConfigManager(t)
	// Restore the package-level broadcasters RegisterDashboardRoutes wires.
	t.Cleanup(func() { accounts.SetBroadcaster(nil); requestlog.SetBroadcaster(nil) })
	tenantKey, err := cm.CreateAPIKey("sec-test", "", "", "")
	if err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	mux := http.NewServeMux()
	RegisterDashboardRoutes(mux, cm)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, cm.DashboardPassword(), tenantKey
}

func TestWS_OriginChecks(t *testing.T) {
	srv, pass, _ := secEnv(t)
	host := strings.TrimPrefix(srv.URL, "http://")
	// status performs a raw upgrade request and returns the HTTP status
	// (101 means the handshake succeeded).
	status := func(origin, pw string) int {
		req, _ := http.NewRequest("GET", srv.URL+"/ws", nil)
		req.Header.Set("Connection", "Upgrade")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Sec-WebSocket-Version", "13")
		req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if pw != "" {
			req.SetBasicAuth("admin", pw)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.Body != nil {
			resp.Body.Close()
		}
		return resp.StatusCode
	}
	if st := status("https://evil.example", pass); st != 403 {
		t.Fatalf("cross-site upgrade: %d, want 403", st)
	}
	if st := status("http://"+host+".evil.example", pass); st != 403 {
		t.Fatalf("lookalike origin: %d, want 403", st)
	}
	if st := status("http://"+host, pass); st != 101 {
		t.Fatalf("same-origin upgrade: %d, want 101", st)
	}
	// Without an Origin our guard lets the request through (x/net/websocket
	// itself then insists on an Origin; that is the library's own policy).
	if st := status("", pass); st == 401 {
		t.Fatalf("no-Origin request with credentials must pass auth, got %d", st)
	}
	if st := status("http://"+host, ""); st != 401 {
		t.Fatalf("no credentials: %d, want 401", st)
	}
	if st := status("https://evil.example", ""); st != 401 {
		t.Fatalf("no credentials + evil origin: %d, want 401", st)
	}
}

func TestSameOriginOrNone_Table(t *testing.T) {
	cases := []struct {
		origin, host string
		want         bool
	}{
		{"", "gw:8900", true},
		{"http://gw:8900", "gw:8900", true},
		{"https://GW:8900", "gw:8900", true},
		{"http://gw:8901", "gw:8900", false},
		{"http://evil.example", "gw:8900", false},
		{"null", "gw:8900", false},
		{"://bad", "gw:8900", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/ws", nil)
		r.Host = c.host
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := sameOriginOrNone(r); got != c.want {
			t.Errorf("origin %q host %q: got %v want %v", c.origin, c.host, got, c.want)
		}
	}
}

// Every mutating dashboard route refuses unauthenticated callers and
// refuses a tenant key / proxy secret presented as the Basic password,
// without running its handler (the tenant list stays untouched).
func TestMutatingRoutes_RequireDashboardCredentials(t *testing.T) {
	srv, _, tenantKey := secEnv(t)
	routes := []struct{ method, path string }{
		{"POST", "/api/keys"}, {"DELETE", "/api/keys?key=" + "anything"}, {"PUT", "/api/keys"},
		{"DELETE", "/api/accounts?email=a@b.c"}, {"POST", "/api/accounts"}, {"POST", "/api/accounts/toggle?email=a@b.c&disabled=true"},
		{"DELETE", "/api/logs"}, {"POST", "/api/logs"},
		{"POST", "/api/warmup?email=a@b.c"}, {"POST", "/api/warmup-all"}, {"POST", "/api/refresh-quotas"},
		{"POST", "/oauth/login"}, {"POST", "/oauth/callback?code=x&state=y"}, {"PUT", "/oauth/callback"},
	}
	do := func(method, path string, auth func(*http.Request)) int {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(`{"name":"x"}`))
		if auth != nil {
			auth(req)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, rt := range routes {
		if st := do(rt.method, rt.path, nil); st != 401 {
			t.Errorf("%s %s without credentials: %d, want 401", rt.method, rt.path, st)
		}
		if st := do(rt.method, rt.path, func(r *http.Request) { r.SetBasicAuth("admin", tenantKey) }); st != 401 {
			t.Errorf("%s %s with tenant key as password: %d, want 401", rt.method, rt.path, st)
		}
		if st := do(rt.method, rt.path, func(r *http.Request) { r.SetBasicAuth(tenantKey, "") }); st != 401 {
			t.Errorf("%s %s with tenant key as user: %d, want 401", rt.method, rt.path, st)
		}
		if st := do(rt.method, rt.path, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tenantKey) }); st != 401 {
			t.Errorf("%s %s with tenant key as Bearer: %d, want 401", rt.method, rt.path, st)
		}
	}
}

func TestDashboardRoutes_NoCORSAllowOrigin(t *testing.T) {
	srv, pass, _ := secEnv(t)
	for _, p := range []string{"/api/keys", "/api/accounts", "/dashboard"} {
		req, _ := http.NewRequest("GET", srv.URL+p, nil)
		req.SetBasicAuth("admin", pass)
		req.Header.Set("Origin", "https://evil.example")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if v := resp.Header.Get("Access-Control-Allow-Origin"); v != "" {
			t.Errorf("%s sets Access-Control-Allow-Origin=%q", p, v)
		}
	}
}

func TestBootstrap_BearerSchemeCaseInsensitive(t *testing.T) {
	srv, _, tenantKey := secEnv(t)
	for _, scheme := range []string{"Bearer", "bearer", "BEARER"} {
		req, _ := http.NewRequest("GET", srv.URL+"/api/bootstrap", nil)
		req.Header.Set("Authorization", scheme+" "+tenantKey)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("scheme %q: status %d", scheme, resp.StatusCode)
		}
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/bootstrap", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("wrong key: %d", resp.StatusCode)
	}
}
