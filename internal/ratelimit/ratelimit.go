// Package ratelimit ports superrelay (v1)'s server.go rate-limit-header
// logic (applyRateLimitHeaders, setSplitTokenRateLimitHeaders,
// pickFiveHourAndWeekly, isFiveHourWindow/isWeeklyWindow, bucketFraction,
// bucketResetSec, isNative3PModelExhausted) into its own leaf package
// rather than leaving it inside internal/orchestrate: both
// internal/orchestrate (every successful/failed attempt sets these
// headers) and the future internal/httpapi's /v1/usage endpoint
// (bucketFraction/pickFiveHourAndWeekly, not yet ported — that endpoint's
// own JSON-body shape, bucketToUsageWindow, is deliberately left for
// whenever /v1/usage itself gets ported) need the identical quota-bucket
// math, and orchestrate must not become httpapi's dependency just to
// reach it. Same single-ownership rationale as internal/toolsig/
// internal/toolargs in this migration.
package ratelimit

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/fakeids"
)

// minResetAhead: bucketResetSec never returns a timestamp that's already
// at-or-past "now" — claude.exe's own client-side state machine drops a
// rate-limit window whose resetsAt <= now (and clears the whole display
// if that was the "representative" one) — it expects a reset to always
// lie in the future. Our upstream (Antigravity) bucket's own ResetTime
// occasionally arrives already elapsed or only seconds away by the time
// it reaches the client, so clamp forward to "now + a safety margin"
// instead of surfacing a stale/past instant.
const minResetAhead = 2 * time.Minute

// IsNative3PModelExhausted reports whether acc's own 3P (Claude/GPT)
// quota group is exhausted. Native 3P model names (claude-sonnet-4-6
// etc.) pass straight through tiering's ResolveTieringAndEffort
// unchanged — fine when the account has 3P quota, but on a free-tier
// account that's commonly 0% while Gemini itself sits at 100%, which
// otherwise means requests for those models hit a guaranteed 429 instead
// of falling back to the Gemini-mapped equivalent other claude-* models
// already get.
func IsNative3PModelExhausted(acc *accounts.Record) bool {
	if acc == nil {
		return false
	}
	for _, g := range acc.Quotas {
		if !strings.Contains(strings.ToLower(g.DisplayName), "claude") {
			continue
		}
		for _, b := range g.Buckets {
			if b.RemainingFraction > 0.05 {
				return false
			}
		}
		return true
	}
	// Quotas were probed (retrieveUserQuotaSummary ran) but came back
	// with no Claude/GPT group at all — on a free-tier account that means
	// 3P was never provisioned, not "quota unknown, assume available".
	return len(acc.Quotas) > 0
}

// isFiveHourWindow and isWeeklyWindow recognize CloudCode's quota-window
// name regardless of separator style ("5h"/"5_hour"/"5-hour",
// "week"/"7d"/"7_day"/"7-day").
func isFiveHourWindow(win string) bool {
	return strings.Contains(win, "5h") || strings.Contains(win, "5_h") || strings.Contains(win, "5-h") || strings.Contains(win, "18000")
}

func isWeeklyWindow(win string) bool {
	return strings.Contains(win, "week") || strings.Contains(win, "7d") || strings.Contains(win, "7_d") || strings.Contains(win, "7-d")
}

// pickFiveHourAndWeekly reports BOTH account-level windows at once,
// matching the real five_hour/seven_day top-level fields confirmed via
// MITM capture. It prioritizes the "Gemini Models" quota group (which
// powers 99% of requests), unless the currently active request is
// explicitly targeting a 3P Claude/GPT model.
func pickFiveHourAndWeekly(acc *accounts.Record, modelName string) (fiveHour, weekly *accounts.QuotaBucket) {
	if acc == nil || len(acc.Quotas) == 0 {
		return nil, nil
	}

	targetGroupName := "gemini"
	if strings.HasPrefix(modelName, "claude-") || strings.HasPrefix(modelName, "gpt-") {
		targetGroupName = "claude"
	}

	scan := func(groupFilter string) (fh, wk *accounts.QuotaBucket) {
		for i := range acc.Quotas {
			g := &acc.Quotas[i]
			if groupFilter != "" && !strings.Contains(strings.ToLower(g.DisplayName), groupFilter) {
				continue
			}
			for j := range g.Buckets {
				b := &g.Buckets[j]
				win := strings.ToLower(b.Window)
				if isFiveHourWindow(win) {
					fh = b
				} else if isWeeklyWindow(win) {
					wk = b
				}
			}
			if fh != nil || wk != nil {
				return fh, wk
			}
		}
		return nil, nil
	}

	// 1. Try matching preferred quota group first.
	if fh, wk := scan(targetGroupName); fh != nil || wk != nil {
		return fh, wk
	}
	// 2. Fallback: select Gemini group explicitly if present.
	if fh, wk := scan("gemini"); fh != nil || wk != nil {
		return fh, wk
	}
	// 3. Fallback: first available group with buckets.
	return scan("")
}

// bucketFraction returns 1-RemainingFraction clamped to [0,1], or 0 for nil.
func bucketFraction(b *accounts.QuotaBucket) float64 {
	if b == nil {
		return 0
	}
	f := 1.0 - b.RemainingFraction
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func bucketResetSec(b *accounts.QuotaBucket, fallback time.Duration) int64 {
	now := time.Now()
	if b != nil && b.ResetTime != "" {
		var t time.Time
		var err error
		if t, err = time.Parse(time.RFC3339Nano, b.ResetTime); err != nil {
			t, err = time.Parse(time.RFC3339, b.ResetTime)
		}
		if err == nil {
			if t.After(now.Add(minResetAhead)) {
				return t.Unix()
			}
			return now.Add(minResetAhead).Unix()
		}
	}
	return now.Add(fallback).Unix()
}

// setSplitTokenRateLimitHeaders sets the input/output-specific token
// rate-limit headers Anthropic's spec defines alongside (not instead of)
// the aggregated anthropic-ratelimit-tokens-* triplet. These are
// synthetic, like the aggregated numbers they split — the gateway has no
// real per-tenant quota to report — using an 80/20 input/output split as
// a representative approximation of a typical Claude-Code-shaped
// workload (large context reads, comparatively short generations).
func setSplitTokenRateLimitHeaders(header http.Header, tokLimit int, remainingFrac float64, reset time.Time) {
	inputLimit := int(float64(tokLimit) * 0.8)
	outputLimit := tokLimit - inputLimit
	resetStr := reset.UTC().Format(time.RFC3339)
	header.Set("anthropic-ratelimit-input-tokens-limit", fmt.Sprintf("%d", inputLimit))
	header.Set("anthropic-ratelimit-input-tokens-remaining", fmt.Sprintf("%d", int(float64(inputLimit)*remainingFrac)))
	header.Set("anthropic-ratelimit-input-tokens-reset", resetStr)
	header.Set("anthropic-ratelimit-output-tokens-limit", fmt.Sprintf("%d", outputLimit))
	header.Set("anthropic-ratelimit-output-tokens-remaining", fmt.Sprintf("%d", int(float64(outputLimit)*remainingFrac)))
	header.Set("anthropic-ratelimit-output-tokens-reset", resetStr)
}

// ApplyHeaders sets the Anthropic unified rate-limit headers to match a
// REAL subscriber account's shape, captured live via MITM against
// api.anthropic.com: separate -5h-status/-utilization/-reset and
// -7d-status/-utilization/-reset, plus a top-level -status/-reset
// mirroring whichever window is binding, and -representative-claim
// naming that window. overage-status stays "rejected" with
// disabled-reason "org_level_disabled", matching what a real account
// with no purchased extra credits actually sends.
func ApplyHeaders(header http.Header, acc *accounts.Record, modelName string) {
	if acc == nil {
		// A tenant on google_auth_type "api_key" never goes through the
		// OAuth account pool, so there is no Record and no real quota
		// numbers to report. Without ANY anthropic-ratelimit-unified-*
		// headers, Claude Code's /usage progress bars disappear entirely.
		now := time.Now()
		header.Set("anthropic-ratelimit-unified-5h-status", "allowed")
		header.Set("anthropic-ratelimit-unified-5h-utilization", "0.05")
		header.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", now.Add(5*time.Hour).Unix()))
		header.Set("anthropic-ratelimit-unified-7d-status", "allowed")
		header.Set("anthropic-ratelimit-unified-7d-utilization", "0.05")
		header.Set("anthropic-ratelimit-unified-7d-reset", fmt.Sprintf("%d", now.Add(7*24*time.Hour).Unix()))
		header.Set("anthropic-ratelimit-unified-status", "allowed")
		header.Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
		header.Set("anthropic-ratelimit-unified-reset", fmt.Sprintf("%d", now.Add(5*time.Hour).Unix()))
		header.Set("anthropic-ratelimit-unified-overage-status", "rejected")
		header.Set("anthropic-ratelimit-unified-overage-disabled-reason", "org_level_disabled")
		const reqLimit, tokLimit = 1000, 1000000
		header.Set("anthropic-ratelimit-requests-limit", fmt.Sprintf("%d", reqLimit))
		header.Set("anthropic-ratelimit-requests-remaining", fmt.Sprintf("%d", int(float64(reqLimit)*0.95)))
		header.Set("anthropic-ratelimit-requests-reset", now.Add(5*time.Hour).UTC().Format(time.RFC3339))
		header.Set("anthropic-ratelimit-tokens-limit", fmt.Sprintf("%d", tokLimit))
		header.Set("anthropic-ratelimit-tokens-remaining", fmt.Sprintf("%d", int(float64(tokLimit)*0.95)))
		header.Set("anthropic-ratelimit-tokens-reset", now.Add(5*time.Hour).UTC().Format(time.RFC3339))
		setSplitTokenRateLimitHeaders(header, tokLimit, 0.95, now.Add(5*time.Hour))
		header.Set("anthropic-organization-id", fakeids.OrgID)
		header.Set("anthropic-workspace-id", fakeids.WorkspaceID)
		return
	}

	// Quotas starts empty for every account until its first successful
	// FetchQuota (there's no on-disk cache) — on a fresh gateway restart,
	// whichever request lands first can easily beat the background
	// warmup. Fetch synchronously the first time so this response isn't
	// missing anthropic-ratelimit-unified-* entirely; once warm, the
	// async staleness path below is enough.
	if len(acc.Quotas) == 0 {
		_ = accounts.Manager().FetchQuota(acc.Email)
		if fresh, ok := accounts.Manager().GetAccount(acc.Email); ok && fresh != nil {
			acc = fresh
		}
	}
	// 5h quota depletes/resets ~34x faster than the weekly one, so a
	// snapshot older than ~45s is already misleading.
	accounts.Manager().TriggerAsyncQuotaRefresh(acc.Email, 45*time.Second)

	header.Set("anthropic-organization-id", fakeids.OrgID)
	header.Set("anthropic-workspace-id", fakeids.WorkspaceID)

	fiveHourBucket, weeklyBucket := pickFiveHourAndWeekly(acc, modelName)
	if fiveHourBucket == nil && weeklyBucket == nil {
		now := time.Now()
		header.Set("anthropic-ratelimit-unified-5h-status", "allowed")
		header.Set("anthropic-ratelimit-unified-5h-utilization", "0.05")
		header.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", now.Add(5*time.Hour).Unix()))
		header.Set("anthropic-ratelimit-unified-7d-status", "allowed")
		header.Set("anthropic-ratelimit-unified-7d-utilization", "0.05")
		header.Set("anthropic-ratelimit-unified-7d-reset", fmt.Sprintf("%d", now.Add(7*24*time.Hour).Unix()))
		header.Set("anthropic-ratelimit-unified-status", "allowed")
		header.Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
		header.Set("anthropic-ratelimit-unified-reset", fmt.Sprintf("%d", now.Add(5*time.Hour).Unix()))
		header.Set("anthropic-ratelimit-unified-overage-status", "rejected")
		header.Set("anthropic-ratelimit-unified-overage-disabled-reason", "org_level_disabled")
		const reqLimit, tokLimit = 1000, 1000000
		header.Set("anthropic-ratelimit-requests-limit", fmt.Sprintf("%d", reqLimit))
		header.Set("anthropic-ratelimit-requests-remaining", fmt.Sprintf("%d", int(float64(reqLimit)*0.95)))
		header.Set("anthropic-ratelimit-requests-reset", now.Add(5*time.Hour).UTC().Format(time.RFC3339))
		header.Set("anthropic-ratelimit-tokens-limit", fmt.Sprintf("%d", tokLimit))
		header.Set("anthropic-ratelimit-tokens-remaining", fmt.Sprintf("%d", int(float64(tokLimit)*0.95)))
		header.Set("anthropic-ratelimit-tokens-reset", now.Add(5*time.Hour).UTC().Format(time.RFC3339))
		setSplitTokenRateLimitHeaders(header, tokLimit, 0.95, now.Add(5*time.Hour))
		return
	}

	fiveHourFrac := bucketFraction(fiveHourBucket)
	weeklyFrac := bucketFraction(weeklyBucket)
	fiveHourReset := bucketResetSec(fiveHourBucket, 5*time.Hour)
	weeklyReset := bucketResetSec(weeklyBucket, 7*24*time.Hour)

	status := func(frac float64) string {
		if frac >= 1 {
			return "rejected"
		}
		if frac >= 0.95 {
			return "allowed_warning"
		}
		return "allowed"
	}

	if fiveHourBucket != nil {
		header.Set("anthropic-ratelimit-unified-5h-status", status(fiveHourFrac))
		header.Set("anthropic-ratelimit-unified-5h-utilization", fmt.Sprintf("%.2f", fiveHourFrac))
		header.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", fiveHourReset))
	} else {
		header.Set("anthropic-ratelimit-unified-5h-status", "allowed")
		header.Set("anthropic-ratelimit-unified-5h-utilization", "0.05")
		header.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", fiveHourReset))
	}
	if weeklyBucket != nil {
		header.Set("anthropic-ratelimit-unified-7d-status", status(weeklyFrac))
		header.Set("anthropic-ratelimit-unified-7d-utilization", fmt.Sprintf("%.2f", weeklyFrac))
		header.Set("anthropic-ratelimit-unified-7d-reset", fmt.Sprintf("%d", weeklyReset))
	} else {
		header.Set("anthropic-ratelimit-unified-7d-status", "allowed")
		header.Set("anthropic-ratelimit-unified-7d-utilization", "0.05")
		header.Set("anthropic-ratelimit-unified-7d-reset", fmt.Sprintf("%d", weeklyReset))
	}

	// Whichever window is currently more exhausted is "representative".
	// Strict > (not >=) keeps this consistent with the /usage JSON body's
	// own is_active tie-break (fiveHourFrac >= weeklyFrac).
	claim, topStatus, topReset := "five_hour", status(fiveHourFrac), fiveHourReset
	if fiveHourBucket == nil && weeklyBucket != nil {
		claim, topStatus, topReset = "seven_day", status(weeklyFrac), weeklyReset
	} else if weeklyBucket != nil && weeklyFrac > fiveHourFrac {
		claim, topStatus, topReset = "seven_day", status(weeklyFrac), weeklyReset
	}
	header.Set("anthropic-ratelimit-unified-status", topStatus)
	header.Set("anthropic-ratelimit-unified-representative-claim", claim)
	header.Set("anthropic-ratelimit-unified-reset", fmt.Sprintf("%d", topReset))

	header.Set("anthropic-ratelimit-unified-overage-status", "rejected")
	header.Set("anthropic-ratelimit-unified-overage-disabled-reason", "org_level_disabled")

	// Standard per-window headers for any generic Anthropic SDK client
	// (not just Claude Code, which only reads the unified-* headers
	// above). Falls back to the weekly bucket's frac/reset when the 5h
	// one isn't available, so an account with ONLY a weekly bucket still
	// gets these rather than silently none at all.
	standardFrac, standardReset, haveStandard := fiveHourFrac, fiveHourReset, fiveHourBucket != nil
	if !haveStandard && weeklyBucket != nil {
		standardFrac, standardReset, haveStandard = weeklyFrac, weeklyReset, true
	}
	if haveStandard {
		const reqLimit, tokLimit = 1000, 1000000
		header.Set("anthropic-ratelimit-requests-limit", fmt.Sprintf("%d", reqLimit))
		header.Set("anthropic-ratelimit-requests-remaining", fmt.Sprintf("%d", int(float64(reqLimit)*(1-standardFrac))))
		header.Set("anthropic-ratelimit-requests-reset", time.Unix(standardReset, 0).UTC().Format(time.RFC3339))
		header.Set("anthropic-ratelimit-tokens-limit", fmt.Sprintf("%d", tokLimit))
		header.Set("anthropic-ratelimit-tokens-remaining", fmt.Sprintf("%d", int(float64(tokLimit)*(1-standardFrac))))
		header.Set("anthropic-ratelimit-tokens-reset", time.Unix(standardReset, 0).UTC().Format(time.RFC3339))
		setSplitTokenRateLimitHeaders(header, tokLimit, 1-standardFrac, time.Unix(standardReset, 0))
	}
}
