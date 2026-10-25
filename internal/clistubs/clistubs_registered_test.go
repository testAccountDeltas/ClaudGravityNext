package clistubs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// registeredPatterns reads RegisterHandlers' own source, so a newly added
// stub is exercised without anyone remembering to extend a test table.
func registeredPatterns(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("clistubs.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range regexp.MustCompile(`(?m)^\thandle\("([^"]+)"`).FindAllSubmatch(src, -1) {
		out = append(out, string(m[1]))
	}
	if len(out) < 50 {
		t.Fatalf("found only %d registrations: the extraction pattern is stale", len(out))
	}
	return out
}

// Every stub must answer a CLI probe with 200 and a JSON body, carry the
// identifiers the official SDKs expect, and never need credentials.
func TestEveryRegisteredStubAnswersWithValidJSON(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHandlers(mux)
	for _, pat := range registeredPatterns(t) {
		path := regexp.MustCompile(`\{[^}]+\}`).ReplaceAllString(pat, "x")
		t.Run(pat, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d", path, rec.Code)
			}
			if !json.Valid(rec.Body.Bytes()) {
				t.Fatalf("body is not JSON: %s", rec.Body.String())
			}
			h := rec.Header()
			if h.Get("anthropic-version") == "" || h.Get("request-id") == "" || h.Get("request-id") != h.Get("x-request-id") {
				t.Fatalf("missing SDK headers: %v", h)
			}
			if rec.Header().Get("Set-Cookie") != "" || strings.Contains(rec.Body.String(), "sk-") {
				t.Fatalf("stub leaked something credential-shaped: %s", rec.Body.String())
			}
		})
	}
}

func TestMiddlewaresWrapEveryStubInOrder(t *testing.T) {
	var order []string
	tag := func(name string) func(http.Handler) http.Handler {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	mux := http.NewServeMux()
	RegisterHandlers(mux, tag("outer"), tag("inner"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/hello", nil))
	if rec.Code != 200 || strings.Join(order, ",") != "outer,inner" {
		t.Fatalf("code=%d order=%v", rec.Code, order)
	}
}

func TestUsageStubShape(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHandlers(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/oauth/usage", nil))
	var got map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"five_hour", "seven_day", "limits", "extra_usage", "spend"} {
		if _, ok := got[k]; !ok {
			t.Errorf("usage lacks %q", k)
		}
	}
	if limits, _ := got["limits"].([]interface{}); len(limits) != 4 {
		t.Fatalf("limits = %v", got["limits"])
	}
}

func TestDomainInfoEchoesTheDomainAndAllowsFetching(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHandlers(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/web/domain_info?domain=a.example", nil))
	var got struct {
		Domain   string `json:"domain"`
		CanFetch bool   `json:"can_fetch"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Domain != "a.example" || !got.CanFetch {
		t.Fatalf("%+v err=%v", got, err)
	}
}

func TestFeedbackIdsAreUnique(t *testing.T) {
	mux := http.NewServeMux()
	RegisterHandlers(mux)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/feedback", nil))
		if seen[rec.Body.String()] {
			t.Fatal("feedback id repeated")
		}
		seen[rec.Body.String()] = true
	}
}
