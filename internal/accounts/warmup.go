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
	"time"

	"claudgravity/internal/warmup"
)

// WarmupSingleAccount sends a lightweight query to activate a single account's
// 7-day window. Bulk multi-account warmup was deliberately removed to avoid
// synchronized farm-like bursts across accounts.
func (am *AccountManager) WarmupSingleAccount(ctx context.Context, email string) (string, error) {
	am.mu.RLock()
	acc, ok := am.accounts[email]
	disabled := ok && acc.Disabled
	am.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("account %s not found", email)
	}
	if disabled {
		return "", fmt.Errorf("account %s is disabled", email)
	}

	token, err := am.RefreshAccessToken(email)
	if err != nil {
		return "", fmt.Errorf("token refresh failed: %w", err)
	}

	am.mu.RLock()
	projectID := acc.ProjectID
	am.mu.RUnlock()

	rawRequest := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"You are a helpful assistant."}]}}`
	wrapped := warmup.WrapAntigravityPayload("gemini-2.5-flash", []byte(rawRequest), projectID, 0, "")

	var lastErr error
	var lastStatus int
	for _, host := range warmup.AntigravityHosts {
		url := host + "/v1internal:generateContent"
		req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(string(wrapped)))
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", AntigravityUserAgent)

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

		lastStatus = resp.StatusCode
		lastErr = fmt.Errorf("host %s returned %d: %s", host, resp.StatusCode, string(respBytes))
		if resp.StatusCode != http.StatusTooManyRequests {
			break
		}
	}

	am.mu.Lock()
	acc.LastError = fmt.Sprintf("Warmup failed: %v", lastErr)
	am.mu.Unlock()
	if lastStatus == http.StatusForbidden {
		am.RecordAccountError(email, lastStatus, lastErr.Error(), time.Now().Add(24*time.Hour))
	} else if lastStatus != 0 {
		am.RecordAccountError(email, lastStatus, lastErr.Error(), time.Time{})
	}
	return "", lastErr
}
