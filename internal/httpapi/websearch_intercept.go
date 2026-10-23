// Part of the httpapi package — see errors.go for the package doc
// comment. The direct Claude-Code-native-web-search fast-path intercept
// from v1's handleMessages section "2.1" — detection
// (internal/websearch.IsClaudeCodeDirectWebSearch) and execution
// (internal/websearch.HandleClaudeCodeDirectWebSearch) both already
// exist from an earlier phase of this migration; this file is only the
// account-selection wiring between the two, faithfully ported from v1.
package httpapi

import (
	"context"
	"net/http"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
	"claudgravity/internal/logx"
	"claudgravity/internal/tlsfingerprint"
	"claudgravity/internal/websearch"
)

// websearchHTTPClient is this package's own Google-facing client for the
// direct-web-search intercept — a legitimate, dedicated dependency on
// internal/tlsfingerprint (this package really does make an outbound
// Google call for this one fast path) rather than a shared global
// reached across package boundaries, same rationale as
// internal/accounts' and internal/orchestrate's own copies.
var websearchHTTPClient = &http.Client{
	Transport: tlsfingerprint.NewFingerprintedRoundTripper(),
}

// tryDirectWebSearchIntercept handles v1's handleMessages section "2.1":
// Claude Code's own native WebSearch sends a tiny single-turn sub-request
// this gateway can answer locally without a full Gemini round-trip.
// Returns true if it fully handled the response (caller must return
// immediately without writing anything else).
func tryDirectWebSearchIntercept(ctx context.Context, rid uint64, t0 time.Time, clientKey string, tenant config.TenantConfig, bodyBytes []byte, w http.ResponseWriter) bool {
	searchQuery, isSearch := websearch.IsClaudeCodeDirectWebSearch(bodyBytes)
	if !isSearch {
		return false
	}

	var activeAcc *accounts.Record
	var googleToken string
	isAntigravity := true
	projectID := "aicode-consumers"

	if tenant.GoogleAuthType != "api_key" || tenant.GoogleAPIKey == "" || tenant.GoogleAPIKey == "YOUR_GEMINI_API_KEY_HERE" {
		acc, err := accounts.GlobalSelector().SelectAccount(clientKey, "", tenant)
		if err == nil && acc != nil {
			activeAcc = acc
			if acc.ProjectID != "" {
				projectID = acc.ProjectID
			}
			tok, _ := accounts.Manager().RefreshAccessToken(acc.Email)
			googleToken = tok
		}
	} else {
		isAntigravity = false
	}

	accLabel := "DirectAPIKey"
	if activeAcc != nil {
		accLabel = activeAcc.Email
	}
	logx.LogGateway("[#%d] >> INTERCEPT Claude Code Direct WebSearch: query=%q account=%s", rid, searchQuery, accLabel)
	websearch.HandleClaudeCodeDirectWebSearch(
		ctx, rid, t0, clientKey, accLabel, googleToken, isAntigravity,
		activeAcc != nil, projectID, websearchHTTPClient, accounts.AntigravityUserAgent,
		bodyBytes, searchQuery, w, activeAcc,
	)
	return true
}
