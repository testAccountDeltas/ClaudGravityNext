package accounts

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const modelsReply = `{"models":{
 "claude-sonnet-4-6":{"displayName":"Claude Sonnet","modelProvider":"anthropic","maxTokens":200000,"maxOutputTokens":64000,"supportsThinking":true,"thinkingBudget":16000,"minThinkingBudget":1024,"quotaInfo":{"remainingFraction":0.4,"resetTime":"2030-01-01T00:00:00Z"}},
 "bare-model":{}
}}`

// resetCatalog empties the process-wide catalog for the test and puts it back.
func resetCatalog(t *testing.T) {
	t.Helper()
	restore := SetCatalogForTest(nil)
	t.Cleanup(restore)
	globalModelCatalog.mu.Lock()
	globalModelCatalog.fetchedAt = time.Time{}
	globalModelCatalog.mu.Unlock()
}

func catalogAccount(email string) *Record {
	return &Record{Email: email, RefreshToken: "r", AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}
}

func TestFetchAvailableModels_ParsesFillsDefaultsAndCaches(t *testing.T) {
	resetCatalog(t)
	seedAccounts(t, catalogAccount("cat-ok@example.com"))
	var calls atomic.Int32
	var auth string
	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		auth = r.Header.Get("Authorization")
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(modelsReply)), Header: http.Header{}}, nil
	}))

	got, err := globalAccountManager.FetchAvailableModels(false)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %d models, err=%v", len(got), err)
	}
	if auth != "Bearer tok" {
		t.Fatalf("Authorization = %q", auth)
	}
	m, ok := ModelCatalog().GetModel("claude-sonnet-4-6")
	if !ok || m.DisplayName != "Claude Sonnet" || m.MaxOutput != 64000 || !m.Thinking || m.MinThinkingBudget != 1024 || m.QuotaRemaining != 0.4 || m.ResetTime == "" {
		t.Fatalf("model fields not carried over: %+v ok=%v", m, ok)
	}
	if b, _ := ModelCatalog().GetModel("bare-model"); b.DisplayName != "bare-model" {
		t.Fatalf("a model without a display name must show its id, got %q", b.DisplayName)
	}
	if _, ok := ModelCatalog().GetModel("nope"); ok {
		t.Fatal("unknown model reported present")
	}

	// A second call inside the TTL is served from cache; force bypasses it.
	if _, err := globalAccountManager.FetchAvailableModels(false); err != nil || calls.Load() != 1 {
		t.Fatalf("cached call hit the network: calls=%d err=%v", calls.Load(), err)
	}
	if _, err := globalAccountManager.FetchAvailableModels(true); err != nil || calls.Load() != 2 {
		t.Fatalf("forced refresh must refetch: calls=%d err=%v", calls.Load(), err)
	}
	all := ModelCatalog().GetAll()
	all[0].ID = "mutated"
	if ModelCatalog().GetAll()[0].ID == "mutated" {
		t.Fatal("GetAll must return a copy")
	}
}

func TestFetchAvailableModels_Failures(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"http error", 503, `busy`, "HTTP 503"},
		{"no models key", 200, `{"other":1}`, "no 'models'"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resetCatalog(t)
			seedAccounts(t, catalogAccount("cat-fail@example.com"))
			withStubbedHTTPClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: c.status, Body: io.NopCloser(strings.NewReader(c.body)), Header: http.Header{}}, nil
			}))
			if _, err := globalAccountManager.FetchAvailableModels(true); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to mention %q", err, c.want)
			}
			if len(ModelCatalog().GetAll()) != 0 {
				t.Fatal("a failed fetch must leave the catalog untouched")
			}
		})
	}
}

func TestFetchAvailableModels_SkipsUnusableAccounts(t *testing.T) {
	resetCatalog(t)
	off := catalogAccount("cat-off@example.com")
	off.Disabled = true
	seedAccounts(t, off)
	if _, err := globalAccountManager.FetchAvailableModels(true); err == nil || !strings.Contains(err.Error(), "no active") {
		t.Fatalf("with no usable account want a 'no active account' error, got %v", err)
	}
}

func TestFetchAvailableModels_FallsToTheNextAccountWhenARefreshFails(t *testing.T) {
	resetCatalog(t)
	stale := &Record{Email: "cat-stale@example.com", RefreshToken: "bad", ExpiresAt: time.Now().Add(-time.Hour)}
	seedAccounts(t, stale, catalogAccount("cat-good@example.com"))
	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Host, "oauth2") || strings.Contains(r.URL.Host, "googleapis.com") && strings.Contains(r.URL.Path, "token") {
			return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`)), Header: http.Header{}}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(modelsReply)), Header: http.Header{}}, nil
	}))
	if got, err := globalAccountManager.FetchAvailableModels(true); err != nil || len(got) != 2 {
		t.Fatalf("one broken account must not hide the catalog: %d models, err=%v", len(got), err)
	}
}
