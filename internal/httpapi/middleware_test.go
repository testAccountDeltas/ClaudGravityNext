package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCommonAPIMiddleware_OptionsPreflight(t *testing.T) {
	h := CommonAPIMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler must not run on an OPTIONS preflight")
	}))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodOptions, "/v1/messages", nil)
	h.ServeHTTP(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := w.Header().Get("anthropic-organization-id"); got != fakeOrgID {
		t.Errorf("anthropic-organization-id = %q, want %q", got, fakeOrgID)
	}
}

func TestCommonAPIMiddleware_StampsRequestIDAndVersion(t *testing.T) {
	h := CommonAPIMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	h.ServeHTTP(w, r)

	if got := w.Header().Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", got)
	}
	if got := w.Header().Get("request-id"); got == "" {
		t.Error("request-id is empty, want a req_<hex> value")
	}
}

func TestRecoverMiddleware_PanicDoesNotCrashAndReturns500(t *testing.T) {
	h := recoverMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	h.ServeHTTP(w, r) // must not panic out of the test itself

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}
}
