package artifacts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestViewerAndDashboardAPI(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	h := NewHandler(s, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	h.RegisterViewer(mux)
	h.RegisterDashboardAPI(mux, func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Dash") != "ok" {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	})

	// deploy/prepare returns 200 ok.
	req := httptest.NewRequest("POST", "/api/frame/deploy/prepare", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"valid":true`) {
		t.Fatalf("deploy/prepare: %d %s", rec.Code, rec.Body.String())
	}

	// Publish an artifact whose body uses window.claude.hot.ready and </script>.
	req = httptest.NewRequest("POST", "/api/frame/deploy/direct", strings.NewReader(`{
		"title":"Focus Timer",
		"description":"Demo timer",
		"content":"<!doctype html><html><head><title>X</title></head><body><script>window.claude.hot.ready(function(){});</script></body></html>"
	}`))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var dep map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &dep); err != nil {
		t.Fatalf("deploy parse: %v", err)
	}
	slug := dep["slug"].(string)

	for _, path := range []string{"/code/artifact/" + slug, "/artifact/" + slug, "/code/artifact/" + slug + "/"} {
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		body := rec.Body.String()
		if rec.Code != http.StatusOK {
			t.Fatalf("viewer %s: %d", path, rec.Code)
		}
		if !strings.Contains(body, `sandbox="allow-scripts allow-forms allow-downloads allow-modals allow-popups"`) {
			t.Fatalf("viewer %s must sandbox iframe without allow-same-origin: %s", path, body)
		}
		if strings.Contains(body, "allow-same-origin") {
			t.Fatalf("viewer %s must not enable allow-same-origin", path)
		}
		if !strings.Contains(body, "Focus Timer") || !strings.Contains(body, `window.claude=window.claude`) {
			t.Fatalf("viewer %s missing title or window.claude shim", path)
		}
	}

	// Bad/missing slugs return 404.
	for _, bad := range []string{"/code/artifact/", "/code/artifact/not-a-uuid", "/code/artifact/00000000-0000-0000-0000-000000000000"} {
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", bad, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("bad viewer path %s: got %d, want 404", bad, rec.Code)
		}
	}

	// Dashboard list requires auth wrapper; authorized call returns the artifact.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/dashboard/artifacts", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed dashboard list = %d, want 401", rec.Code)
	}
	listReq := httptest.NewRequest("GET", "/api/dashboard/artifacts", nil)
	listReq.Header.Set("X-Dash", "ok")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, listReq)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), slug) || !strings.Contains(rec.Body.String(), "Focus Timer") {
		t.Fatalf("authed dashboard list = %d %s", rec.Code, rec.Body.String())
	}

	// Dashboard delete removes the artifact.
	delReq := httptest.NewRequest("DELETE", "/api/dashboard/artifacts/"+slug, nil)
	delReq.Header.Set("X-Dash", "ok")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, delReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard delete = %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/code/artifact/"+slug, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("viewer after delete = %d, want 404", rec.Code)
	}
}
