package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
	"claudgravity/internal/reqctx"
)

func seedAccount(t *testing.T, email, token string) *accounts.Record {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, email+".json")
	content := fmt.Sprintf(`{"email":%q,"access_token":%q,"refresh_token":"ref"}`, email, token)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test account file: %v", err)
	}
	if err := accounts.InitAccountManager(dir, config.ServerConfig{}); err != nil {
		t.Fatalf("failed to init account manager: %v", err)
	}
	acc, ok := accounts.Manager().GetAccount(email)
	if !ok {
		t.Fatalf("account %s not loaded", email)
	}
	return acc
}

func TestApplyPostFailureCooldown_RPM(t *testing.T) {
	acc := seedAccount(t, "test-rpm@example.com", "tok")
	errBody := `HTTP 429: {"error":{"code":429,"message":"Quota exceeded","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED","metadata":{"quotaLimit":"GenerateRequestsPerMinutePerProject"}}]}}`

	applyPostFailureCooldown(acc, http.StatusTooManyRequests, errors.New(errBody))

	fresh, ok := accounts.Manager().GetAccount(acc.Email)
	if !ok {
		t.Fatalf("account %s not found", acc.Email)
	}
	cd := time.Until(fresh.CooldownUntil)
	if cd < 25*time.Second || cd > 35*time.Second {
		t.Fatalf("expected ~30s cooldown for RPM, got %v", cd)
	}
}

func TestApplyPostFailureCooldown_RetryDelay(t *testing.T) {
	acc := seedAccount(t, "test-retrydelay@example.com", "tok")
	errBody := `HTTP 429: {"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"45s"}]}}`

	applyPostFailureCooldown(acc, http.StatusTooManyRequests, errors.New(errBody))

	fresh, ok := accounts.Manager().GetAccount(acc.Email)
	if !ok {
		t.Fatalf("account %s not found", acc.Email)
	}
	cd := time.Until(fresh.CooldownUntil)
	if cd < 40*time.Second || cd > 50*time.Second {
		t.Fatalf("expected ~45s cooldown from retryDelay, got %v", cd)
	}
}

func TestApplyPostFailureCooldown_ResetTs(t *testing.T) {
	acc := seedAccount(t, "test-resetts@example.com", "tok")
	futureTs := time.Now().Add(75 * time.Second).UTC().Format(time.RFC3339)
	errBody := fmt.Sprintf(`HTTP 429: {"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"metadata":{"quotaResetTimeStamp":%q}}]}}`, futureTs)

	applyPostFailureCooldown(acc, http.StatusTooManyRequests, errors.New(errBody))

	fresh, ok := accounts.Manager().GetAccount(acc.Email)
	if !ok {
		t.Fatalf("account %s not found", acc.Email)
	}
	cd := time.Until(fresh.CooldownUntil)
	if cd < 70*time.Second || cd > 80*time.Second {
		t.Fatalf("expected ~75s cooldown from resetTs, got %v", cd)
	}
}

func TestApplyPostFailureCooldown_MalformedJSON_SafeFallback(t *testing.T) {
	acc := seedAccount(t, "test-malformed@example.com", "tok")
	malformedBodies := []string{
		`HTTP 429: {"error": "not an object"}`,
		`HTTP 429: {"error": {"details": "not an array"}}`,
		`HTTP 429: {broken json`,
		`HTTP 429: `,
		`HTTP 429: {}`,
		`HTTP 429: {"error":{"details":[{"metadata":null,"retryDelay":123}]}}`,
		`HTTP 429: {"error":{"details":[{"retryDelay":"invalid_duration"}]}}`,
	}

	for _, body := range malformedBodies {
		applyPostFailureCooldown(acc, http.StatusTooManyRequests, errors.New(body))
		fresh, ok := accounts.Manager().GetAccount(acc.Email)
		if !ok {
			t.Fatalf("account %s not found", acc.Email)
		}
		cd := time.Until(fresh.CooldownUntil)
		// Safe fallback is 15 minutes
		if cd < 14*time.Minute || cd > 16*time.Minute {
			t.Fatalf("for body %q, expected fallback 15m cooldown, got %v", body, cd)
		}
	}
}

func TestApplyPostFailureCooldown_503And529(t *testing.T) {
	acc := seedAccount(t, "test-503@example.com", "tok")
	applyPostFailureCooldown(acc, http.StatusServiceUnavailable, errors.New("upstream unavailable"))

	fresh, ok := accounts.Manager().GetAccount(acc.Email)
	if !ok {
		t.Fatalf("account not found")
	}
	cd := time.Until(fresh.CooldownUntil)
	if cd < 2*time.Minute || cd > 4*time.Minute {
		t.Fatalf("expected ~3m cooldown for 503, got %v", cd)
	}

	acc2 := seedAccount(t, "test-529@example.com", "tok")
	applyPostFailureCooldown(acc2, 529, errors.New("overloaded"))

	fresh2, ok := accounts.Manager().GetAccount(acc2.Email)
	if !ok {
		t.Fatalf("account not found")
	}
	cd2 := time.Until(fresh2.CooldownUntil)
	if cd2 < 2*time.Minute || cd2 > 4*time.Minute {
		t.Fatalf("expected ~3m cooldown for 529, got %v", cd2)
	}
}

func TestApplyPostFailureCooldown_401InvalidatesToken(t *testing.T) {
	acc := seedAccount(t, "test-401@example.com", "initial-token")
	if acc.AccessToken != "initial-token" {
		t.Fatalf("expected initial-token, got %q", acc.AccessToken)
	}
	applyPostFailureCooldown(acc, http.StatusUnauthorized, errors.New("HTTP 401: Unauthorized"))

	fresh, ok := accounts.Manager().GetAccount(acc.Email)
	if !ok {
		t.Fatalf("account not found")
	}
	if fresh.AccessToken != "" {
		t.Fatalf("expected access token to be invalidated, got %q", fresh.AccessToken)
	}
}

func TestExecute_PreStreamFailover_NonStreaming_RetriesOnNextAccount(t *testing.T) {
	origTransport := httpClient.Transport
	defer func() { httpClient.Transport = origTransport }()

	restoreAccountsHTTP := accounts.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			bodyBytes, _ := io.ReadAll(req.Body)
			tok := "fresh-token-acc2"
			if strings.Contains(string(bodyBytes), "ref1") {
				tok = "fresh-token-acc1"
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"access_token":%q,"expires_in":3600}`, tok))),
				Header:     make(http.Header),
			}, nil
		}),
	})
	defer restoreAccountsHTTP()

	dir := t.TempDir()
	file1 := filepath.Join(dir, "acc1@example.com.json")
	file2 := filepath.Join(dir, "acc2@example.com.json")
	_ = os.WriteFile(file1, []byte(`{"email":"acc1@example.com","access_token":"token-acc1","refresh_token":"ref1"}`), 0644)
	_ = os.WriteFile(file2, []byte(`{"email":"acc2@example.com","access_token":"token-acc2","refresh_token":"ref2"}`), 0644)

	if err := accounts.InitAccountManager(dir, config.ServerConfig{}); err != nil {
		t.Fatalf("InitAccountManager: %v", err)
	}

	var attemptCount int
	var accountsUsed []string
	httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attemptCount++
		auth := req.Header.Get("Authorization")
		accountsUsed = append(accountsUsed, auth)
		if attemptCount == 1 {
			// First attempt returns 429
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`)),
				Header:     make(http.Header),
			}, nil
		}
		// Second attempt succeeds
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"candidates":[{"content":{"parts":[{"text":"hello back"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`)),
			Header:     make(http.Header),
		}, nil
	})

	p := &reqctx.Params{
		RID:           301,
		IsStreaming:   false,
		IsAntigravity: true,
		ServerCfg: config.ServerConfig{
			TTFTTimeoutSeconds: 5,
			ReadTimeoutSeconds: 5,
		},
		RawClaudeBody: []byte(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hello"}]}`),
	}
	st := &reqctx.State{
		ResolvedModelName: "gemini-3.8-flash-medium",
		StartedAt:         time.Now(),
		GeminiBody:        []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	}

	rec := httptest.NewRecorder()
	orch := &Orchestrator{}
	ok, status, err := orch.Execute(context.Background(), p, st, rec)
	if !ok || status != http.StatusOK {
		t.Fatalf("expected failover to succeed on second account, got ok=%v status=%d err=%v", ok, status, err)
	}
	if attemptCount != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", attemptCount)
	}
	if len(accountsUsed) == 2 && accountsUsed[0] == accountsUsed[1] {
		t.Fatalf("expected different accounts on retry, got same account twice: %v", accountsUsed)
	}
}

func TestExecute_PreStreamFailover_Streaming_RetriesOnNextAccount(t *testing.T) {
	origTransport := httpClient.Transport
	defer func() { httpClient.Transport = origTransport }()

	restoreAccountsHTTP := accounts.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			bodyBytes, _ := io.ReadAll(req.Body)
			tok := "fresh-token-s2"
			if strings.Contains(string(bodyBytes), "ref1") {
				tok = "fresh-token-s1"
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(fmt.Sprintf(`{"access_token":%q,"expires_in":3600}`, tok))),
				Header:     make(http.Header),
			}, nil
		}),
	})
	defer restoreAccountsHTTP()

	dir := t.TempDir()
	file1 := filepath.Join(dir, "stream1@example.com.json")
	file2 := filepath.Join(dir, "stream2@example.com.json")
	_ = os.WriteFile(file1, []byte(`{"email":"stream1@example.com","access_token":"token-s1","refresh_token":"ref1"}`), 0644)
	_ = os.WriteFile(file2, []byte(`{"email":"stream2@example.com","access_token":"token-s2","refresh_token":"ref2"}`), 0644)

	if err := accounts.InitAccountManager(dir, config.ServerConfig{}); err != nil {
		t.Fatalf("InitAccountManager: %v", err)
	}

	var attemptCount int
	var accountsUsed []string
	httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		attemptCount++
		auth := req.Header.Get("Authorization")
		accountsUsed = append(accountsUsed, auth)
		if attemptCount == 1 {
			// First attempt returns 503 UNAVAILABLE
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"code":503,"status":"UNAVAILABLE"}}`)),
				Header:     make(http.Header),
			}, nil
		}
		// Second attempt returns SSE stream with done
		sseData := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"chunk 1\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2}}\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(sseData)),
			Header:     make(http.Header),
		}, nil
	})

	p := &reqctx.Params{
		RID:           302,
		IsStreaming:   true,
		IsAntigravity: true,
		ServerCfg: config.ServerConfig{
			PanicTimeoutSeconds:      5,
			TTFTTimeoutSeconds:       5,
			MaxStreamDurationSeconds: 10,
		},
		RawClaudeBody: []byte(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hello stream"}]}`),
	}
	st := &reqctx.State{
		ResolvedModelName: "gemini-3.8-flash-medium",
		StartedAt:         time.Now(),
		GeminiBody:        []byte(`{"contents":[{"role":"user","parts":[{"text":"hello stream"}]}]}`),
	}

	rec := httptest.NewRecorder()
	orch := &Orchestrator{}
	ok, status, err := orch.Execute(context.Background(), p, st, rec)
	if !ok || status != http.StatusOK {
		t.Fatalf("expected streaming failover to succeed on second account, got ok=%v status=%d err=%v", ok, status, err)
	}
	if attemptCount != 2 {
		t.Fatalf("expected exactly 2 attempts, got %d", attemptCount)
	}
	if len(accountsUsed) == 2 && accountsUsed[0] == accountsUsed[1] {
		t.Fatalf("expected different accounts on retry, got same account twice: %v", accountsUsed)
	}
}

