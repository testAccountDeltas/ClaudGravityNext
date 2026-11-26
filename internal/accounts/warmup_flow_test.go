package accounts

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"claudgravity/internal/warmup"
)

// hostScript answers warm-up requests per upstream host (index into
// warmup.AntigravityHosts); quota probes get an empty summary.
type hostScript struct {
	mu    sync.Mutex
	hosts []string
	reply func(hostIdx int) (int, string, error)
}

func (h *hostScript) install(t *testing.T) {
	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "retrieveUserQuotaSummary") {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"groups":[]}`)), Header: http.Header{}}, nil
		}
		if !strings.HasSuffix(r.URL.Path, "/v1internal:generateContent") {
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`?`)), Header: http.Header{}}, nil
		}
		idx := -1
		for i, host := range warmup.AntigravityHosts {
			if strings.HasPrefix(host, r.URL.Scheme+"://"+r.URL.Host) {
				idx = i
			}
		}
		h.mu.Lock()
		h.hosts = append(h.hosts, r.URL.Host)
		h.mu.Unlock()
		code, body, err := h.reply(idx)
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	}))
}

func warmAcc(email string) *Record {
	a := catalogAccount(email)
	a.ProjectID = "proj-1"
	return a
}

func TestWarmupSingleAccount_UnknownAccount(t *testing.T) {
	seedAccounts(t)
	if _, err := globalAccountManager.WarmupSingleAccount(context.Background(), "ghost@example.com"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestWarmupSingleAccount_SuccessClearsTheErrorAndRefreshesQuota(t *testing.T) {
	a := warmAcc("wu-ok@example.com")
	a.LastError = "old failure"
	seedAccounts(t, a)
	(&hostScript{reply: func(int) (int, string, error) { return 200, `{}`, nil }}).install(t)

	msg, err := globalAccountManager.WarmupSingleAccount(context.Background(), a.Email)
	if err != nil || msg == "" {
		t.Fatalf("msg=%q err=%v", msg, err)
	}
	got, _ := globalAccountManager.GetAccount(a.Email)
	if got.LastError != "" || got.LastChecked.IsZero() {
		t.Fatalf("a successful warm-up must clear the error and stamp the time: %+v", got)
	}
}

func TestWarmupSingleAccount_RateLimitedHostFallsThroughToTheNext(t *testing.T) {
	if len(warmup.AntigravityHosts) < 2 {
		t.Skip("needs at least two hosts")
	}
	a := warmAcc("wu-429@example.com")
	seedAccounts(t, a)
	s := &hostScript{reply: func(i int) (int, string, error) {
		if i == 0 {
			return 429, `slow down`, nil
		}
		return 200, `{}`, nil
	}}
	s.install(t)
	if _, err := globalAccountManager.WarmupSingleAccount(context.Background(), a.Email); err != nil {
		t.Fatalf("second host should have saved it: %v", err)
	}
	if len(s.hosts) < 2 {
		t.Fatalf("expected a retry on another host, hosts tried: %v", s.hosts)
	}
}

func TestWarmupSingleAccount_TransportErrorTriesTheNextHost(t *testing.T) {
	if len(warmup.AntigravityHosts) < 2 {
		t.Skip("needs at least two hosts")
	}
	a := warmAcc("wu-net@example.com")
	seedAccounts(t, a)
	s := &hostScript{reply: func(i int) (int, string, error) {
		if i == 0 {
			return 0, "", errors.New("dial tcp: connection refused")
		}
		return 200, `{}`, nil
	}}
	s.install(t)
	if _, err := globalAccountManager.WarmupSingleAccount(context.Background(), a.Email); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestWarmupSingleAccount_HardRejectionStopsAndIsRecorded(t *testing.T) {
	a := warmAcc("wu-403@example.com")
	seedAccounts(t, a)
	s := &hostScript{reply: func(int) (int, string, error) { return 403, `forbidden`, nil }}
	s.install(t)
	_, err := globalAccountManager.WarmupSingleAccount(context.Background(), a.Email)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
	if len(s.hosts) != 1 {
		t.Fatalf("a non-429 rejection must not be retried on other hosts, tried %v", s.hosts)
	}
	got, _ := globalAccountManager.GetAccount(a.Email)
	if !strings.HasPrefix(got.LastError, "Warmup failed") {
		t.Fatalf("LastError = %q", got.LastError)
	}
	if got.ErrorKind != ErrKindForbidden {
		t.Fatalf("ErrorKind = %q, want %q", got.ErrorKind, ErrKindForbidden)
	}
}

func TestWarmupSingleAccount_SkipsDisabledAccount(t *testing.T) {
	a := warmAcc("wu-off@example.com")
	a.Disabled = true
	seedAccounts(t, a)
	withStubbedHTTPClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("disabled account must not hit the network during warmup")
		return nil, nil
	}))
	if _, err := globalAccountManager.WarmupSingleAccount(context.Background(), a.Email); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err = %v, want disabled error", err)
	}
}

func TestWarmupSingleAccount_TokenRefreshFailure(t *testing.T) {
	a := &Record{Email: "wu-tok@example.com", RefreshToken: "dead", ExpiresAt: time.Now().Add(-time.Hour)}
	seedAccounts(t, a)
	withStubbedHTTPClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`)), Header: http.Header{}}, nil
	}))
	if _, err := globalAccountManager.WarmupSingleAccount(context.Background(), a.Email); err == nil || !strings.Contains(err.Error(), "token refresh failed") {
		t.Fatalf("err = %v", err)
	}
}
