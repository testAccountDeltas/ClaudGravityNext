// Part of the accounts package's port from superrelay (v1)'s
// auth_manager.go — see pool.go for the split rationale. This file
// holds OAuth token refresh/exchange and the login-flow CSRF state.
package accounts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/atomicfile"
)

// AntigravityClientID/Secret default to the values this gateway has always
// shipped with (Google's "installed app" OAuth client for Antigravity —
// not secret in the sense of a confidential-client secret, but still not
// something that belongs hardcoded in version-controlled source next to
// the rest of this project's credential files). config.json's
// server.google_oauth_client_id/_secret override these at startup
// (InitAccountManager) if set.
var (
	AntigravityClientID     = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"
	AntigravityClientSecret = ""

	// AntigravityUserAgent is the exact header the real Antigravity client
	// sends on its own CloudCode calls — the literal "1.X.X" is not an
	// unfilled placeholder, that's the string the real client sends.
	// CloudCode gates its answer on this (an outdated version is served a
	// reduced model set / coarser quota view), so this needs bumping in
	// step with Google's shipping Antigravity release when that starts
	// happening. config.json's server.antigravity_user_agent overrides
	// this at startup (InitAccountManager) if set, so it can be bumped
	// without recompiling.
	AntigravityUserAgent = "vscode/1.X.X (Antigravity/4.3.0)"
)

const (
	GoogleTokenEndpoint = "https://oauth2.googleapis.com/token"
	GoogleAuthEndpoint  = "https://accounts.google.com/o/oauth2/v2/auth"
	OAuthRedirectURI    = "http://localhost:8898/oauth/callback"
)

// deadRefreshTokens remembers, per account email, that Google's own OAuth
// server just said "invalid_grant" (the refresh token itself is revoked/
// expired, not a transient failure) — without this, N requests arriving
// concurrently for that account all queue on acc.refreshMu and each
// serially burn up to the full 15s timeout re-making the exact same
// doomed network call, instead of failing instantly after the first one
// already proved it's hopeless.
var (
	deadRefreshTokensMu sync.Mutex
	deadRefreshTokens   = make(map[string]time.Time)
)

const deadRefreshTokenCooldown = 5 * time.Minute

// refreshNow is the clock RefreshAccessToken uses for token expiry and
// negative-cache deadlines; a var so tests can drive it deterministically.
var refreshNow = time.Now

func (am *AccountManager) RefreshAccessToken(email string) (string, error) {
	am.mu.RLock()
	acc, ok := am.accounts[email]
	am.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("account %s not found", email)
	}
	am.mu.RLock()
	hasRefresh := acc.RefreshToken != ""
	am.mu.RUnlock()
	if !hasRefresh {
		return "", fmt.Errorf("account %s has no refresh token", email)
	}

	acc.refreshMu.Lock()
	defer acc.refreshMu.Unlock()

	// Re-check under this account's own lock: either we still have a valid
	// cached token (no network call needed at all), or a concurrent caller
	// already refreshed it while we were waiting for the lock above.
	am.mu.RLock()
	cachedToken := acc.AccessToken
	cachedExpiry := acc.ExpiresAt
	curRefresh := acc.RefreshToken
	am.mu.RUnlock()
	const refreshSafetyMargin = 60 * time.Second
	if cachedToken != "" && refreshNow().Add(refreshSafetyMargin).Before(cachedExpiry) {
		return cachedToken, nil
	}

	deadRefreshTokensMu.Lock()
	deadUntil, isDead := deadRefreshTokens[email]
	deadRefreshTokensMu.Unlock()
	if isDead {
		if refreshNow().Before(deadUntil) {
			// Generic wording now that this cache also covers a short-lived
			// network-failure cooldown, not just a definitively invalid
			// refresh token.
			return "", fmt.Errorf("refresh for %s recently failed, retry after cooldown", email)
		}
		deadRefreshTokensMu.Lock()
		delete(deadRefreshTokens, email)
		deadRefreshTokensMu.Unlock()
	}

	data := url.Values{}
	data.Set("client_id", AntigravityClientID)
	data.Set("client_secret", AntigravityClientSecret)
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", curRefresh)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", GoogleTokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")

	resp, err := httpClient().Do(req)
	if err != nil {
		// An audit caught that a genuine network-level failure here
		// (timeout, DNS failure, connection refused — this call has its
		// own 15s ctx deadline above) was never cached at all, unlike an
		// explicit OAuth error response. acc.refreshMu is held for this
		// entire function, so N parallel callers for the same account
		// (common for subagents) serialize one after another, each
		// re-attempting and re-timing-out independently — N*15s of total
		// blocking instead of one real attempt. A SHORT negative cache
		// (far shorter than deadRefreshTokenCooldown's 5 minutes, which is
		// reserved for a definitively broken account per the OAuth-error
		// case below) lets concurrent waiters fail fast without
		// meaningfully delaying recovery once the blip passes.
		deadRefreshTokensMu.Lock()
		deadRefreshTokens[email] = refreshNow().Add(10 * time.Second)
		deadRefreshTokensMu.Unlock()
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		// RFC 6749 §5.2 non-retriable OAuth errors trigger dead-token cooldown.
		// HTTP 429 triggers a short 10s cooldown to avoid hammering Google.
		if resp.StatusCode == http.StatusTooManyRequests {
			deadRefreshTokensMu.Lock()
			deadRefreshTokens[email] = refreshNow().Add(10 * time.Second)
			deadRefreshTokensMu.Unlock()
			am.MarkAccountCooldown(email, 10*time.Second)
			globalSelector.UnstickAccount(email)
			return "", fmt.Errorf("refresh rate-limited (429): %s", string(bodyBytes))
		}
		oauthErr := gjson.GetBytes(bodyBytes, "error").String()
		if oauthErr == "invalid_grant" || oauthErr == "invalid_request" || oauthErr == "unauthorized_client" || oauthErr == "invalid_client" {
			deadRefreshTokensMu.Lock()
			deadRefreshTokens[email] = refreshNow().Add(deadRefreshTokenCooldown)
			deadRefreshTokensMu.Unlock()
			// An audit caught that deadRefreshTokens alone only makes
			// THIS function fail fast on a dead account — it says nothing
			// to the account SELECTOR, which keeps choosing this same
			// dead account for every new incoming user request (sticky
			// affinity or scoring has no idea it's unusable) until the
			// retry loop excludes it for that one attempt, burning a real
			// slot out of maxAttempts on every single request. Put it in
			// the selector's own cooldown too so it's skipped up front.
			am.MarkAccountCooldown(email, deadRefreshTokenCooldown)
			// Needs a manual re-login; surface it (sticky for a day) so the
			// dashboard says so instead of just showing a short cooldown.
			am.RecordAccountError(email, resp.StatusCode, "OAuth "+oauthErr+": "+string(bodyBytes), refreshNow().Add(24*time.Hour))
			// A Round 15 audit caught MarkAccountCooldown alone doesn't
			// unstick this account from any session it's already pinned
			// to — sticky-session affinity (globalSelector) keeps routing
			// that session's later requests back to the same now-known-
			// dead account until its sticky entry happens to expire on
			// its own, even though cooldown correctly excludes it from
			// being picked FRESH. A fatal, non-transient OAuth error
			// (revoked/expired grant, bad client) means this account is
			// not coming back without the user re-authenticating, so
			// detach it immediately.
			globalSelector.UnstickAccount(email)
		}
		return "", fmt.Errorf("refresh failed (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	newAccToken := gjson.GetBytes(bodyBytes, "access_token").String()
	expSeconds := gjson.GetBytes(bodyBytes, "expires_in").Int()
	if expSeconds <= 0 {
		expSeconds = 3599
	}
	newRefreshToken := gjson.GetBytes(bodyBytes, "refresh_token").String()

	am.mu.Lock()
	acc.AccessToken = newAccToken
	acc.ExpiresAt = refreshNow().Add(time.Duration(expSeconds) * time.Second)
	filePath := acc.FilePath
	if newRefreshToken != "" {
		acc.RefreshToken = newRefreshToken
	}
	am.mu.Unlock()

	if newRefreshToken != "" && filePath != "" {
		updateAuthFile(filePath, func(data []byte) ([]byte, error) {
			return sjson.SetBytes(data, "refresh_token", newRefreshToken)
		})
	}

	am.triggerProjectIDProbe(email, newAccToken)

	return newAccToken, nil
}

var (
	oauthStateMu  sync.Mutex
	oauthStates   = make(map[string]time.Time) // state -> expiry
	oauthStateTTL = 10 * time.Minute
)

// newOAuthState mints a random, single-use CSRF state token for the OAuth
// login flow and remembers it server-side until consumeOAuthState redeems
// (or it expires).
func newOAuthState() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	state := hex.EncodeToString(b)

	oauthStateMu.Lock()
	defer oauthStateMu.Unlock()
	now := time.Now()
	for s, exp := range oauthStates {
		if now.After(exp) {
			delete(oauthStates, s)
		}
	}
	oauthStates[state] = now.Add(oauthStateTTL)
	return state
}

// NewOAuthState is the exported entry point for the dashboard's login
// handler (v1's ui.go called the package-private newOAuthState directly,
// since both lived in the same package main).
func NewOAuthState() string { return newOAuthState() }

// consumeOAuthState validates and single-use-consumes a state token from
// the OAuth callback. Returns false for missing/unknown/expired/replayed
// state — the callback must then refuse to exchange the code.
func consumeOAuthState(state string) bool {
	if state == "" {
		return false
	}
	oauthStateMu.Lock()
	defer oauthStateMu.Unlock()
	exp, ok := oauthStates[state]
	delete(oauthStates, state) // single-use regardless of outcome
	return ok && time.Now().Before(exp)
}

// ConsumeOAuthState is the exported entry point for the dashboard's
// callback handler.
func ConsumeOAuthState(state string) bool { return consumeOAuthState(state) }

func BuildGoogleAuthURL(state string) string {
	scopes := []string{
		"https://www.googleapis.com/auth/cloud-platform",
		"https://www.googleapis.com/auth/userinfo.email",
		"https://www.googleapis.com/auth/userinfo.profile",
		"https://www.googleapis.com/auth/cclog",
		"https://www.googleapis.com/auth/experimentsandconfigs",
	}

	params := url.Values{}
	params.Set("access_type", "offline")
	params.Set("client_id", AntigravityClientID)
	params.Set("prompt", "consent")
	params.Set("redirect_uri", OAuthRedirectURI)
	params.Set("response_type", "code")
	params.Set("scope", strings.Join(scopes, " "))
	params.Set("state", state)

	return GoogleAuthEndpoint + "?" + params.Encode()
}

func ExchangeAuthCode(code string) (*Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	data := url.Values{}
	data.Set("code", code)
	data.Set("client_id", AntigravityClientID)
	data.Set("client_secret", AntigravityClientSecret)
	data.Set("redirect_uri", OAuthRedirectURI)
	data.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, "POST", GoogleTokenEndpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")

	resp, err := httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("code exchange failed: %s", string(bodyBytes))
	}

	accToken := gjson.GetBytes(bodyBytes, "access_token").String()
	refToken := gjson.GetBytes(bodyBytes, "refresh_token").String()

	// Get user info email
	uReq, err := http.NewRequestWithContext(ctx, "GET", "https://www.googleapis.com/oauth2/v1/userinfo?alt=json", nil)
	if err != nil {
		return nil, err
	}
	uReq.Header.Set("Authorization", "Bearer "+accToken)
	uResp, err := httpClient().Do(uReq)
	if err != nil {
		return nil, err
	}
	defer uResp.Body.Close()

	uBody, _ := io.ReadAll(uResp.Body)
	email := gjson.GetBytes(uBody, "email").String()
	if email == "" {
		return nil, fmt.Errorf("user email missing in response")
	}

	// Persist account
	filePath := filepath.Join(globalAccountManager.authDir, fmt.Sprintf("antigravity-%s.json", email))
	fileData := map[string]interface{}{
		"email":         email,
		"project_id":    "aicode-consumers",
		"type":          "antigravity",
		"refresh_token": refToken,
		"access_token":  accToken,
		"disabled":      false,
		"timestamp":     time.Now().UnixMilli(),
	}
	b, _ := json.MarshalIndent(fileData, "", "  ")
	_ = atomicfile.WriteFile(filePath, b)

	record := &Record{
		Email:        email,
		ProjectID:    "aicode-consumers",
		RefreshToken: refToken,
		AccessToken:  accToken,
		ExpiresAt:    time.Now().Add(1 * time.Hour),
		FilePath:     filePath,
	}

	globalAccountManager.mu.Lock()
	globalAccountManager.accounts[email] = record
	globalAccountManager.mu.Unlock()

	_ = globalAccountManager.FetchQuota(email)
	// Probe for the real GCP project_id immediately rather than waiting 1 hour for first refresh
	go globalAccountManager.triggerProjectIDProbe(email, accToken)

	return record, nil
}
