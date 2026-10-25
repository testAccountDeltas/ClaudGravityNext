package dashboard

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"claudgravity/internal/accounts"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// failOnGoogle makes any outbound Google call fail the test and counts them.
func failOnGoogle(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	restore := accounts.SetHTTPClientForTest(&http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		n.Add(1)
		t.Errorf("unexpected outbound call to %s", r.URL)
		return nil, http.ErrHandlerTimeout
	})})
	t.Cleanup(restore)
	return &n
}

func TestOAuthLogin_RedirectsToGoogleWithFreshState(t *testing.T) {
	rec := httptest.NewRecorder()
	handleOAuthLogin(rec, httptest.NewRequest("GET", "/oauth/login", nil))
	if rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil || loc.Host != "accounts.google.com" {
		t.Fatalf("Location = %q (%v)", rec.Header().Get("Location"), err)
	}
	state := loc.Query().Get("state")
	if len(state) != 48 {
		t.Fatalf("state = %q", state)
	}
	if !accounts.ConsumeOAuthState(state) {
		t.Fatal("the state placed in the redirect must be redeemable by the callback")
	}
}

func TestOAuthCallback_RefusesBadStateBeforeAnyExchange(t *testing.T) {
	calls := failOnGoogle(t)
	for name, target := range map[string]string{
		"no state":      "/oauth/callback?code=abc",
		"unknown state": "/oauth/callback?state=forged&code=abc",
	} {
		rec := httptest.NewRecorder()
		handleOAuthCallback(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Invalid or expired OAuth state") {
			t.Errorf("%s: status=%d body=%q", name, rec.Code, rec.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("a forged callback must never reach Google, saw %d calls", calls.Load())
	}
}

func TestOAuthCallback_MissingCodeAndReplay(t *testing.T) {
	calls := failOnGoogle(t)
	state := accounts.NewOAuthState()
	rec := httptest.NewRecorder()
	handleOAuthCallback(rec, httptest.NewRequest("GET", "/oauth/callback?state="+state, nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Missing authorization code") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	// The state was spent by the first attempt, so a second one is a replay.
	rec = httptest.NewRecorder()
	handleOAuthCallback(rec, httptest.NewRequest("GET", "/oauth/callback?code=abc&state="+state, nil))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Invalid or expired OAuth state") {
		t.Fatalf("replay: status=%d body=%q", rec.Code, rec.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatalf("no exchange may happen, saw %d calls", calls.Load())
	}
}

func TestOAuthCallback_ExchangeFailureIsReported(t *testing.T) {
	restore := accounts.SetHTTPClientForTest(&http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: http.Header{}, Body: http.NoBody}, nil
	})})
	t.Cleanup(restore)
	state := accounts.NewOAuthState()
	rec := httptest.NewRecorder()
	handleOAuthCallback(rec, httptest.NewRequest("GET", "/oauth/callback?code=bad&state="+state, nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "OAuth exchange error") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestOAuthSuccessPage_EscapesTheEmail(t *testing.T) {
	page := oauthSuccessPage(`<script>alert(1)</script>"@x.com`)
	if strings.Contains(page, "<script>") {
		t.Fatalf("email reached the page unescaped:\n%s", page)
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Fatalf("escaped email missing:\n%s", page)
	}
	if !strings.Contains(oauthSuccessPage("ok@example.com"), "<b>ok@example.com</b>") {
		t.Fatal("a normal address must be shown as is")
	}
}
