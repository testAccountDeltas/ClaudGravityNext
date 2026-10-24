// Part of the accounts package's port from superrelay (v1)'s
// auth_manager.go — see pool.go for the split rationale. This file
// holds the background GCP project-id discovery (loadCodeAssist/
// onboardUser), ported from clirelay's internal/auth/antigravity/auth.go
// (FetchAccountDetails/OnboardUser), which solves the identical
// discovery problem for the identical upstream.
package accounts

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/logx"
)

const (
	CloudCodeLoadCodeAssistEndpoint = "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"
	CloudCodeOnboardUserEndpoint    = "https://cloudcode-pa.googleapis.com/v1internal:onboardUser"
)

var projectIDProbeInFlight sync.Map // email -> struct{}

// triggerProjectIDProbe kicks off a background, one-time discovery of the
// account's REAL project id via loadCodeAssist/onboardUser, instead of
// silently relying on the generic fallbackProjectID forever. Fired
// asynchronously so a normal request's token refresh never waits on it —
// onboardUser alone can poll for up to 10s. Gated on ProjectIDProbed so it
// only ever runs once per account, mirroring the "don't re-enter onboardUser"
// guard in clirelay's antigravity executor (that path was observed thrashing
// production with auth writes + reloads without one).
func (am *AccountManager) triggerProjectIDProbe(email, accessToken string) {
	am.mu.RLock()
	acc, ok := am.accounts[email]
	probed := ok && acc.ProjectIDProbed
	am.mu.RUnlock()
	if !ok || probed {
		return
	}
	if _, loaded := projectIDProbeInFlight.LoadOrStore(email, struct{}{}); loaded {
		return
	}
	go func() {
		defer projectIDProbeInFlight.Delete(email)
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		discovered, err := discoverAntigravityProjectID(ctx, accessToken)

		am.mu.Lock()
		// Only latch ProjectIDProbed (the permanent "never try again" gate)
		// on an outcome that actually resolved something — a transient
		// failure (network blip, Google 429/500, a not-yet-ready token)
		// must NOT burn the one-shot gate forever: a real bug found live
		// had every account in the pool permanently stuck on
		// fallbackProjectID ("aicode-consumers", which isn't even a project
		// most accounts have IAM permission to use) because this used to
		// set ProjectIDProbed = true unconditionally, including on error.
		// Leaving it false on error lets the NEXT RefreshAccessToken
		// (naturally throttled by the token's own ~1h expiry) retry instead
		// of giving up after exactly one attempt for the account's entire
		// lifetime.
		if err == nil && discovered != "" && discovered != acc.ProjectID {
			logx.LogGateway("[AccountManager] %s: discovered real project_id %q (was %q)", email, discovered, acc.ProjectID)
			acc.ProjectID = discovered
			acc.ProjectIDProbed = true
		} else if err != nil {
			logx.LogGateway("[AccountManager] %s: project_id probe failed, keeping %q, will retry on next token refresh: %v", email, acc.ProjectID, err)
		} else {
			logx.LogGateway("[AccountManager] %s: project_id probe confirmed %q", email, acc.ProjectID)
			acc.ProjectIDProbed = true
		}
		filePath, projectID, probed := acc.FilePath, acc.ProjectID, acc.ProjectIDProbed
		am.mu.Unlock()
		// Disk I/O moved outside am.mu: this used to run the ReadFile +
		// WriteFile round-trip to the account's auth JSON while still
		// holding the SAME global lock every other account operation
		// (routing, quota refresh, token refresh) needs — a slow disk
		// here stalled every concurrent subagent's request routing for as
		// long as the write took.
		persistAccountFields(filePath, projectID, probed)
	}()
}

// persistAccountFields writes project_id/project_id_probed back to the
// account's own auth JSON file. Only those two fields are touched — every
// other key in the file (tokens, timestamps, whatever a future version
// adds) passes through untouched. Takes a snapshot of the fields rather
// than the live *Record so callers can do this disk I/O without holding
// am.mu.
func persistAccountFields(filePath, projectID string, probed bool) {
	updateAuthFile(filePath, func(data []byte) ([]byte, error) {
		updated, err := sjson.SetBytes(data, "project_id", projectID)
		if err != nil {
			return nil, err
		}
		return sjson.SetBytes(updated, "project_id_probed", probed)
	})
}

// discoverAntigravityProjectID retrieves the account's real GCP project id via
// loadCodeAssist, onboarding it (onboardUser, polling for completion) if
// loadCodeAssist reports none yet.
func discoverAntigravityProjectID(ctx context.Context, accessToken string) (string, error) {
	loadBody := []byte(`{"metadata":{"ideType":"ANTIGRAVITY","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, CloudCodeLoadCodeAssistEndpoint, bytes.NewReader(loadBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", AntigravityUserAgent)
	req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
	req.Header.Set("Client-Metadata", `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`)

	resp, err := httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("loadCodeAssist: %w", err)
	}
	loadRespBytes, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("loadCodeAssist HTTP %d: %s", resp.StatusCode, string(loadRespBytes))
	}

	loadResp := gjson.ParseBytes(loadRespBytes)
	projectID := projectIDFromNode(loadResp.Get("cloudaicompanionProject"))
	if projectID != "" {
		return projectID, nil
	}

	defaultTierID := "legacy-tier"
	loadResp.Get("allowedTiers").ForEach(func(_, tier gjson.Result) bool {
		if tier.Get("isDefault").Bool() {
			if id := tier.Get("id").String(); id != "" {
				defaultTierID = id
				return false
			}
		}
		return true
	})

	return onboardAntigravityUser(ctx, accessToken, defaultTierID)
}

// onboardAntigravityUser polls onboardUser until Google reports the
// onboarding operation done, then extracts the newly-provisioned project id.
func onboardAntigravityUser(ctx context.Context, accessToken, tierID string) (string, error) {
	reqBody, _ := sjson.SetBytes(
		[]byte(`{"metadata":{"ideType":"ANTIGRAVITY","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}}`),
		"tierId", tierID,
	)

	const maxAttempts = 5
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, CloudCodeOnboardUserEndpoint, bytes.NewReader(reqBody))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", AntigravityUserAgent)
		req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
		req.Header.Set("Client-Metadata", `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`)

		resp, err := httpClient().Do(req)
		if err != nil {
			return "", fmt.Errorf("onboardUser: %w", err)
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("onboardUser HTTP %d: %s", resp.StatusCode, string(bodyBytes))
		}

		parsed := gjson.ParseBytes(bodyBytes)
		if parsed.Get("done").Bool() {
			projectID := projectIDFromNode(parsed.Get("response.cloudaicompanionProject"))
			if projectID == "" {
				return "", fmt.Errorf("onboardUser completed without a project id")
			}
			return projectID, nil
		}

		if attempt < maxAttempts {
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return "", ctx.Err()
			case <-timer.C:
			}
		}
	}
	return "", fmt.Errorf("onboardUser: did not complete after %d attempts", maxAttempts)
}

// projectIDFromNode handles both wire shapes Google uses for this field: a
// bare string, or {"id": "..."}.
func projectIDFromNode(node gjson.Result) string {
	if node.Type == gjson.String {
		return strings.TrimSpace(node.String())
	}
	if id := node.Get("id").String(); id != "" {
		return strings.TrimSpace(id)
	}
	return ""
}
