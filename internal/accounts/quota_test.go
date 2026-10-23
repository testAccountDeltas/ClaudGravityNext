package accounts

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockBroadcaster struct {
	mu           sync.Mutex
	broadcastCnt int
	lastAccounts []*Record
}

func (m *mockBroadcaster) BroadcastAccountDeleted(email string) {}
func (m *mockBroadcaster) BroadcastAccounts(accounts []*Record) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.broadcastCnt++
	m.lastAccounts = accounts
}

func TestFetchQuota_ProbesSandboxFirst(t *testing.T) {
	const email = "quota-sandbox@example.com"
	acc := &Record{
		Email:       email,
		AccessToken: "test-token",
		ExpiresAt:   time.Now().Add(time.Hour),
		ProjectID:   fallbackProjectID,
	}
	seedAccounts(t, acc)

	var probedHost string
	mockResp := `{
		"groups": [
			{
				"displayName": "Gemini Models",
				"buckets": [
					{
						"bucketId": "gemini-weekly",
						"window": "weekly",
						"remainingFraction": 0.8,
						"resetTime": "` + time.Now().Add(48*time.Hour).Format(time.RFC3339) + `"
					},
					{
						"bucketId": "gemini-5h",
						"window": "5h",
						"remainingFraction": 0.9,
						"resetTime": "` + time.Now().Add(2*time.Hour).Format(time.RFC3339) + `"
					}
				]
			}
		]
	}`

	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if probedHost == "" {
			probedHost = r.URL.Host
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	}))

	err := globalAccountManager.FetchQuota(email)
	if err != nil {
		t.Fatalf("FetchQuota failed: %v", err)
	}

	if probedHost != "daily-cloudcode-pa.sandbox.googleapis.com" {
		t.Fatalf("FetchQuota must probe sandbox host first, got %q", probedHost)
	}

	globalAccountManager.mu.RLock()
	quotas := acc.Quotas
	globalAccountManager.mu.RUnlock()

	if len(quotas) == 0 {
		t.Fatal("acc.Quotas must be populated after successful FetchQuota")
	}
}

func TestFetchQuota_ExhaustedGeminiSetsCooldownAndBlocks(t *testing.T) {
	const email = "quota-exhausted@example.com"
	resetTime := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	acc := &Record{
		Email:       email,
		AccessToken: "test-token",
		ExpiresAt:   time.Now().Add(time.Hour),
		ProjectID:   fallbackProjectID,
	}
	seedAccounts(t, acc)

	mockResp := fmt.Sprintf(`{
		"groups": [
			{
				"displayName": "Gemini Models",
				"buckets": [
					{
						"bucketId": "gemini-weekly",
						"window": "weekly",
						"remainingFraction": 0.20,
						"resetTime": "%s"
					},
					{
						"bucketId": "gemini-5h",
						"window": "5h",
						"remainingFraction": 0.0,
						"resetTime": "%s"
					}
				]
			}
		]
	}`, resetTime.Add(72*time.Hour).Format(time.RFC3339), resetTime.Format(time.RFC3339))

	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	}))

	mb := &mockBroadcaster{}
	SetBroadcaster(mb)
	t.Cleanup(func() { SetBroadcaster(nil) })

	err := globalAccountManager.FetchQuota(email)
	if err != nil {
		t.Fatalf("FetchQuota failed: %v", err)
	}

	globalAccountManager.mu.RLock()
	cd := acc.CooldownUntil
	globalAccountManager.mu.RUnlock()

	if cd.IsZero() || !cd.Equal(resetTime) {
		t.Fatalf("CooldownUntil must match bucket resetTime %v, got %v", resetTime, cd)
	}

	if hasRemainingQuota(acc) {
		t.Fatal("account with 0% 5h quota in cooldown must not be selected (hasRemainingQuota must return false)")
	}

	if frac := getEffectiveQuotaFraction(acc); frac != 0.0 {
		t.Fatalf("effective quota fraction for exhausted account in cooldown must be 0.0, got %v", frac)
	}

	mb.mu.Lock()
	cnt := mb.broadcastCnt
	mb.mu.Unlock()
	if cnt == 0 {
		t.Fatal("broadcaster.BroadcastAccounts must be called when FetchQuota updates quotas")
	}
}

func TestFetchQuota_HealthyQuotaClearsCooldown(t *testing.T) {
	const email = "quota-healthy@example.com"
	acc := &Record{
		Email:         email,
		AccessToken:   "test-token",
		ExpiresAt:     time.Now().Add(time.Hour),
		ProjectID:     fallbackProjectID,
		CooldownUntil: time.Now().Add(10 * time.Minute), // active cooldown should be cleared by healthy quota
	}
	seedAccounts(t, acc)

	mockResp := `{
		"groups": [
			{
				"displayName": "Gemini Models",
				"buckets": [
					{
						"bucketId": "gemini-weekly",
						"window": "weekly",
						"remainingFraction": 0.85,
						"resetTime": "` + time.Now().Add(96*time.Hour).Format(time.RFC3339) + `"
					},
					{
						"bucketId": "gemini-5h",
						"window": "5h",
						"remainingFraction": 0.95,
						"resetTime": "` + time.Now().Add(4*time.Hour).Format(time.RFC3339) + `"
					}
				]
			}
		]
	}`

	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	}))

	err := globalAccountManager.FetchQuota(email)
	if err != nil {
		t.Fatalf("FetchQuota failed: %v", err)
	}

	globalAccountManager.mu.RLock()
	cd := acc.CooldownUntil
	globalAccountManager.mu.RUnlock()

	if !cd.IsZero() {
		t.Fatalf("CooldownUntil must be cleared on healthy quota, got %v", cd)
	}

	if !hasRemainingQuota(acc) {
		t.Fatal("healthy account must satisfy hasRemainingQuota")
	}
}

func TestFetchQuota_FallbackOnSandboxFailure(t *testing.T) {
	const email = "quota-fallback@example.com"
	acc := &Record{
		Email:       email,
		AccessToken: "test-token",
		ExpiresAt:   time.Now().Add(time.Hour),
		ProjectID:   fallbackProjectID,
	}
	seedAccounts(t, acc)

	hostsAttempted := make([]string, 0)
	var mu sync.Mutex

	mockResp := `{
		"groups": [
			{
				"displayName": "Gemini Models",
				"buckets": [
					{
						"bucketId": "gemini-weekly",
						"window": "weekly",
						"remainingFraction": 0.5
					}
				]
			}
		]
	}`

	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		hostsAttempted = append(hostsAttempted, r.URL.Host)
		count := len(hostsAttempted)
		mu.Unlock()

		if count == 1 {
			// First host (sandbox) fails with 503
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("sandbox 503")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}

		// Second host succeeds
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(mockResp)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	}))

	err := globalAccountManager.FetchQuota(email)
	if err != nil {
		t.Fatalf("FetchQuota failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(hostsAttempted) < 2 {
		t.Fatalf("FetchQuota must fall back to second host when first returns 503, attempts: %v", hostsAttempted)
	}
	if hostsAttempted[0] != "daily-cloudcode-pa.sandbox.googleapis.com" {
		t.Errorf("expected first attempt on sandbox, got %v", hostsAttempted[0])
	}
	if hostsAttempted[1] != "daily-cloudcode-pa.googleapis.com" {
		t.Errorf("expected second attempt on daily, got %v", hostsAttempted[1])
	}
}
