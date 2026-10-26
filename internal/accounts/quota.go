// Part of the accounts package's port from superrelay (v1)'s
// auth_manager.go — see pool.go for the split rationale. This file
// holds quota fetching/refresh.
package accounts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sort"
	"sync"
	"time"
)

// QuotaHosts is the ordered list of upstream CloudCode hosts probed for
// retrieveUserQuotaSummary. Sandbox host first: model inference and user
// traffic run against daily-cloudcode-pa.sandbox.googleapis.com (or
// daily-cloudcode-pa.googleapis.com), and quota depletion for Antigravity
// accounts is tracked exclusively in that environment. Production
// cloudcode-pa.googleapis.com is kept as a final fallback.
var QuotaHosts = []string{
	"https://daily-cloudcode-pa.sandbox.googleapis.com",
	"https://daily-cloudcode-pa.googleapis.com",
	"https://cloudcode-pa.googleapis.com",
}

const CloudCodeQuotaEndpoint = "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary"

func (am *AccountManager) FetchQuota(email string) error {
	am.mu.RLock()
	acc, ok := am.accounts[email]
	am.mu.RUnlock()

	if !ok {
		return fmt.Errorf("account not found")
	}

	// acc.AccessToken/ExpiresAt/ProjectID are written elsewhere under
	// am.mu.Lock() (e.g. RefreshAccessToken itself) — reading them here
	// without holding am.mu.RLock() first raced with those writers
	// whenever a subagent request triggered a token refresh concurrently
	// with this quota poll.
	am.mu.RLock()
	accToken := acc.AccessToken
	expiresAt := acc.ExpiresAt
	projectID := acc.ProjectID
	am.mu.RUnlock()

	if accToken == "" || time.Now().After(expiresAt) {
		tok, err := am.RefreshAccessToken(email)
		if err != nil {
			am.mu.Lock()
			acc.LastError = err.Error()
			am.mu.Unlock()
			am.RecordAccountError(email, statusFromErr(err), err.Error(), time.Time{})
			return err
		}
		accToken = tok
	}

	// Prepare payloads: if projectID is a real, discovered GCP project (not the
	// generic "aicode-consumers" fallback), try sending it first, then fall back to
	// empty payload "{}" which asks Google for the account's own user-level quota.
	var payloads [][]byte
	if projectID != "" && projectID != fallbackProjectID {
		if pJSON, err := json.Marshal(map[string]string{"project": projectID}); err == nil {
			payloads = append(payloads, pJSON)
		}
	}
	payloads = append(payloads, []byte("{}"))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var lastErr error
	var respBytes []byte

hostLoop:
	for _, host := range QuotaHosts {
		endpoint := host + "/v1internal:retrieveUserQuotaSummary"
		for _, bodyBytes := range payloads {
			req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(bodyBytes))
			if err != nil {
				lastErr = err
				continue
			}
			req.Header.Set("Authorization", "Bearer "+accToken)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", AntigravityUserAgent)

			resp, err := httpClient().Do(req)
			if err != nil {
				lastErr = err
				break // network failure on this host, try next host
			}
			b, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil {
				lastErr = err
				break
			}
			if resp.StatusCode == http.StatusOK {
				respBytes = b
				lastErr = nil
				break hostLoop
			}
			lastErr = fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, host, string(b))
			if resp.StatusCode == http.StatusUnauthorized {
				am.InvalidateAccessToken(email)
				break hostLoop
			}
			if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusNotFound {
				// 403/400/404 on project-scoped payload, retry with empty payload on same host
				continue
			}
			// 429 or 5xx, try next host
			break
		}
	}

	if lastErr != nil {
		am.mu.Lock()
		acc.LastError = lastErr.Error()
		am.mu.Unlock()
		am.RecordAccountError(email, statusFromErr(lastErr), lastErr.Error(), time.Time{})
		return lastErr
	}

	var parsed struct {
		Groups []struct {
			DisplayName string `json:"displayName"`
			Description string `json:"description"`
			Buckets     []struct {
				BucketID          string   `json:"bucketId"`
				DisplayName       string   `json:"displayName"`
				Window            string   `json:"window"`
				ResetTime         string   `json:"resetTime"`
				RemainingFraction *float64 `json:"remainingFraction"`
				Description       string   `json:"description"`
			} `json:"buckets"`
		} `json:"groups"`
	}

	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		// Malformed body: keep the previous snapshot and cooldowns untouched.
		err = fmt.Errorf("quota response is not valid JSON: %w", err)
		am.mu.Lock()
		acc.LastError = err.Error()
		am.mu.Unlock()
		return err
	}

	var groups []QuotaGroup
	for _, g := range parsed.Groups {
		var buckets []QuotaBucket
		for _, b := range g.Buckets {
			// Absent (or negative = garbage) fraction is "unknown", never 0:
			// reading it as 0 cooled the whole account for 15 minutes. Values
			// above 1 are clamped.
			qb := QuotaBucket{
				BucketID:    b.BucketID,
				DisplayName: b.DisplayName,
				Window:      b.Window,
				ResetTime:   b.ResetTime,
				Description: b.Description,
			}
			switch f := b.RemainingFraction; {
			case f == nil || *f < 0:
				qb.FractionUnknown = true
			case *f > 1:
				qb.RemainingFraction = 1
			default:
				qb.RemainingFraction = *f
			}
			buckets = append(buckets, qb)
		}
		groups = append(groups, QuotaGroup{
			DisplayName: g.DisplayName,
			Description: g.Description,
			Buckets:     buckets,
		})
	}

	// Quota-derived account-wide cooldown: see quotaBlockUntil (any exhausted
	// non-Claude bucket blocks the account until that bucket resets).
	effectiveCooldown := quotaBlockUntil(groups, time.Now())

	am.mu.Lock()
	acc.Quotas = groups
	acc.LastChecked = time.Now()
	clearTransientErrorLocked(acc)
	// A sticky error (403 ban, revoked token) set its own cooldown; a green
	// quota poll must not erase it, so the later of the two wins.
	if stickyErrorKinds[acc.ErrorKind] && acc.ErrorUntil.After(effectiveCooldown) {
		effectiveCooldown = acc.ErrorUntil
	}
	if time.Now().Before(acc.hardCooldownUntil) && acc.hardCooldownUntil.After(effectiveCooldown) {
		effectiveCooldown = acc.hardCooldownUntil // a 429/503/refresh-failure cooldown outlives a green poll
	}
	acc.CooldownUntil = effectiveCooldown
	acc.LastError = ""
	am.mu.Unlock()

	if b := currentBroadcaster(); b != nil {
		b.BroadcastAccounts(am.GetAccounts())
	}

	return nil
}

var quotaRefreshInFlight sync.Map // email -> struct{}

// TriggerAsyncQuotaRefresh kicks off a background FetchQuota for email if its
// last successful check is older than maxAge and no refresh is already running
// for that account. It never blocks the caller — the current request still
// uses whatever quota snapshot is already cached; the next one gets the fresh
// numbers. This exists because the 5-hour quota bucket depletes/resets far
// faster than the weekly one, so a snapshot taken once at warmup (the only
// previous trigger besides the on-demand /v1/usage endpoints) goes stale
// within minutes and the anthropic-ratelimit-unified-5h-* headers end up
// frozen — which is exactly why the CLI's 5h progress bar never moved while
// the weekly one (which barely changes within the staleness window) looked fine.
func (am *AccountManager) TriggerAsyncQuotaRefresh(email string, maxAge time.Duration) {
	am.mu.RLock()
	acc, ok := am.accounts[email]
	am.mu.RUnlock()
	if !ok {
		return
	}
	am.mu.RLock()
	last := acc.LastChecked
	am.mu.RUnlock()
	if time.Since(last) < maxAge {
		return
	}
	if _, loaded := quotaRefreshInFlight.LoadOrStore(email, struct{}{}); loaded {
		return
	}
	go func() {
		defer quotaRefreshInFlight.Delete(email)
		_ = am.FetchQuota(email)
	}()
}

// RefreshAllQuotasStaggered refreshes the accounts one at a time with a random
// pause between them. A process start used to ask Google for every account's
// quota at the same instant (and the gateway was restarted often); a real
// client does not do that.
func (am *AccountManager) RefreshAllQuotasStaggered(minGap, maxGap time.Duration) {
	am.mu.RLock()
	var emails []string
	for e := range am.accounts {
		emails = append(emails, e)
	}
	am.mu.RUnlock()
	sort.Strings(emails)
	for i, e := range emails {
		if i > 0 {
			time.Sleep(minGap + time.Duration(rand.Int63n(int64(maxGap-minGap)+1)))
		}
		_ = am.FetchQuota(e)
	}
}

// RefreshAllQuotas refreshes every account's quota concurrently. FetchQuota
// only ever holds am.mu for brief reads/writes, never across the actual
// network round-trip, so refreshing N accounts one at a time bought nothing
// but N sequential round-trips (noticeably slow on the dashboard's "Refresh
// Quotas" button once there are more than a couple of accounts) — safe to
// fire them all at once instead.
func (am *AccountManager) RefreshAllQuotas() {
	am.mu.RLock()
	var emails []string
	for e := range am.accounts {
		emails = append(emails, e)
	}
	am.mu.RUnlock()

	var wg sync.WaitGroup
	for _, e := range emails {
		wg.Add(1)
		go func(email string) {
			defer wg.Done()
			_ = am.FetchQuota(email)
		}(e)
	}
	wg.Wait()
}
