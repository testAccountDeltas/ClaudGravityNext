package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRootFallbackDoesNotPassthroughByDefault(t *testing.T) {
	h := newRootFallback(false)
	for _, path := range []string{"/api/claude_code/foo", "/api/unknown", "/whatever"} {
		req := httptest.NewRequest(http.MethodGet, "http://localhost:8900"+path, nil)
		req.Header.Set("Authorization", "Bearer sk-tenant")
		req.Header.Set("x-api-key", "sk-tenant")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, rec.Code)
		}
		var body struct {
			Type  string                `json:"type"`
			Error struct{ Type string } `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Type != "error" || body.Error.Type != "not_found_error" {
			t.Fatalf("%s: body = %s", path, rec.Body.String())
		}
	}
}

func TestRootFallbackRedirectsBrowserRoot(t *testing.T) {
	rec := httptest.NewRecorder()
	newRootFallback(false)(rec, httptest.NewRequest(http.MethodGet, "http://localhost:8900/", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/dashboard" {
		t.Fatalf("got %d %q", rec.Code, rec.Header().Get("Location"))
	}
}
