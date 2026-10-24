// Part of the accounts package's port from superrelay (v1)'s
// auth_manager.go/warmup.go — see pool.go for the split rationale.
//
// WarmupSingleAccount/WarmupAllFreshAccounts/needsGeminiWeeklyWarmup are
// ported from v1's warmup.go, NOT pool.go/quota.go like the rest of this
// package: v1 kept them in warmup.go because they're *AccountManager
// methods that also need internal/warmup's WrapAntigravityPayload +
// AntigravityHosts. Phase 1 of this migration ported warmup.go's
// account-independent pieces into internal/warmup and explicitly
// deferred these three (documented in internal/warmup/errors.go's
// package doc comment and ROADMAP_PROGRESS.md) because they're methods
// on *AccountManager / take a *Record, which didn't exist until Phase 2.
// internal/accounts existing now is exactly the trigger that comment
// named for closing the deferral — these live here, not in
// internal/warmup, because they need direct access to am.accounts (the
// unexported map) to read/write acc.LastError/LastChecked, the same
// reason DeleteAccount/ToggleAccountDisabled/FetchQuota are methods on
// this package's own AccountManager rather than free functions.
package accounts

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"claudgravity/internal/warmup"
)

// needsGeminiWeeklyWarmup reports whether acc's Gemini weekly quota bucket
// still sits at (effectively) 100% remaining — i.e. it's never been
// activated by a real request, so the dashboard's "warm up stale accounts"
// button should bother touching it at all.
func needsGeminiWeeklyWarmup(acc *Record) bool {
	for _, grp := range acc.Quotas {
		if !strings.Contains(strings.ToLower(grp.DisplayName), "gemini") {
			continue
		}
		for _, b := range grp.Buckets {
			if strings.Contains(strings.ToLower(b.Window), "week") && b.RemainingFraction >= 0.999 {
				return true
			}
		}
	}
	return false
}

// WarmupSingleAccount sends a 1-token lightweight query to activate the
// account's 7-day window.
func (am *AccountManager) WarmupSingleAccount(ctx context.Context, email string) (string, error) {
	am.mu.RLock()
	acc, ok := am.accounts[email]
	am.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("account %s not found", email)
	}

	token, err := am.RefreshAccessToken(email)
	if err != nil {
		return "", fmt.Errorf("token refresh failed: %w", err)
	}

	am.mu.RLock()
	projectID := acc.ProjectID
	am.mu.RUnlock()

	rawRequest := `{"contents":[{"role":"user","parts":[{"text":"1"}]}],"generationConfig":{"maxOutputTokens":1}}`
	wrapped := warmup.WrapAntigravityPayload("gemini-2.5-flash", []byte(rawRequest), projectID, 0, "")

	var lastErr error
	for _, host := range warmup.AntigravityHosts {
		url := host + "/v1internal:generateContent"
		req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(wrapped)))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", AntigravityUserAgent)
		req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
		req.Header.Set("Client-Metadata", `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`)

		resp, err := httpClient().Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		respBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			am.mu.Lock()
			acc.LastError = ""
			acc.LastChecked = time.Now()
			am.mu.Unlock()
			_ = am.FetchQuota(email)
			return fmt.Sprintf("Успешный прогрев через %s", host), nil
		}

		lastErr = fmt.Errorf("host %s returned %d: %s", host, resp.StatusCode, string(respBytes))
		if resp.StatusCode != http.StatusTooManyRequests {
			break
		}
	}

	am.mu.Lock()
	acc.LastError = fmt.Sprintf("Warmup failed: %v", lastErr)
	am.mu.Unlock()
	return "", lastErr
}

// WarmupAllFreshAccounts warms up every non-disabled account whose Gemini
// weekly window still sits at 100% (never activated), concurrently. Returns
// the emails that were actually warmed and any per-account errors.
func (am *AccountManager) WarmupAllFreshAccounts(ctx context.Context) (warmed []string, errs map[string]string) {
	accs := am.GetAccounts()
	errs = make(map[string]string)

	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, acc := range accs {
		if acc.Disabled || !needsGeminiWeeklyWarmup(acc) {
			continue
		}
		wg.Add(1)
		go func(email string) {
			defer wg.Done()
			_, err := am.WarmupSingleAccount(ctx, email)
			mu.Lock()
			if err != nil {
				errs[email] = err.Error()
			} else {
				warmed = append(warmed, email)
			}
			mu.Unlock()
		}(acc.Email)
	}
	wg.Wait()
	return warmed, errs
}
