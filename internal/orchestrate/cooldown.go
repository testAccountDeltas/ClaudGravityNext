// Part of the orchestrate package — see pipeline.go for the package doc
// comment. Split out of pipeline.go purely to stay under the 400-line
// ceiling: the per-status account cooldown policy.
package orchestrate

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts"
)

// applyPostFailureCooldown mirrors v1's identical cooldown/unstick/
// invalidate-token bookkeeping that ran at BOTH of its two call sites
// (the early-headers-sent branch inside "if success" and the pre-headers
// failure branch below it) — kept as one function so the two call sites
// in Execute above can never drift out of sync with each other again,
// the exact failure mode a Round 15 v1 audit caught between them.
func applyPostFailureCooldown(acc *accounts.Record, status int, err error) {
	applyPostFailureCooldownModel(acc, "", status, err)
}

// applyPostFailureCooldownModel is applyPostFailureCooldown scoped to the
// model the failed attempt ran: a 429 cools only that (account, model) pair
// (an empty model keeps the account-wide behaviour); 403/401/503 stay
// account-level.
func applyPostFailureCooldownModel(acc *accounts.Record, model string, status int, err error) {
	if isModelTurnFailure(err) {
		// An abnormal finishReason / empty turn / truncated stream says
		// nothing about the account's health or quota.
		return
	}
	switch {
	case status == http.StatusTooManyRequests || strings.Contains(fmt.Sprint(err), "RESOURCE_EXHAUSTED"):
		cooldown := 15 * time.Minute
		errStr := fmt.Sprint(err)
		if idx := strings.Index(errStr, "{"); idx != -1 {
			parsed := gjson.Parse(errStr[idx:])
			var resetTs string
			var retryDelayStr string
			isRPM := false
			parsed.Get("error.details").ForEach(func(_, detail gjson.Result) bool {
				quotaLimit := detail.Get("metadata.quotaLimit").String()
				if strings.Contains(quotaLimit, "PerMinute") || strings.Contains(detail.Get("description").String(), "per minute") {
					isRPM = true
				}
				if ts := detail.Get("metadata.quotaResetTimeStamp").String(); ts != "" {
					resetTs = ts
				} else if ts := detail.Get("metadata.quotaResetTimestamp").String(); ts != "" {
					resetTs = ts
				}
				if delay := detail.Get("retryDelay").String(); delay != "" {
					retryDelayStr = delay
				}
				return resetTs == "" || retryDelayStr == ""
			})
			if retryDelayStr != "" {
				if d, err := time.ParseDuration(retryDelayStr); err == nil && d > 0 {
					cooldown = d
				}
			} else if resetTs != "" {
				now := time.Now()
				if t, err := time.Parse(time.RFC3339Nano, resetTs); err == nil && t.After(now) {
					cooldown = t.Sub(now)
				} else if t, err := time.Parse(time.RFC3339, resetTs); err == nil && t.After(now) {
					cooldown = t.Sub(now)
				}
			} else if isRPM {
				cooldown = 30 * time.Second
			}
		}
		accounts.Manager().MarkModelCooldown(acc.Email, model, cooldown)
		accounts.Manager().RecordAccountError(acc.Email, http.StatusTooManyRequests, fmt.Sprint(err), time.Now().Add(cooldown))
		// GAP-ACC-01: without this, the session's sticky-account
		// binding stayed pointed at an account that just got a 15-minute
		// cooldown — a retry later in the SAME session (or a request
		// sent moments later from an already-bound client) could get
		// stuck selecting the same cooling-down account again instead
		// of a healthy one.
		if model == "" {
			accounts.GlobalSelector().UnstickAccount(acc.Email)
		}
		accounts.Manager().TriggerAsyncQuotaRefresh(acc.Email, 0)
	case status == http.StatusForbidden:
		// 403 means Google itself has rejected this account (banned,
		// suspended, region-gated) — not a transient rate limit.
		accounts.Manager().MarkAccountCooldown(acc.Email, 60*time.Minute)
		accounts.Manager().RecordAccountError(acc.Email, status, fmt.Sprint(err), time.Now().Add(60*time.Minute))
		accounts.GlobalSelector().UnstickAccount(acc.Email)
	case status == http.StatusUnauthorized:
		accounts.Manager().InvalidateAccessToken(acc.Email)
		accounts.Manager().RecordAccountError(acc.Email, status, fmt.Sprint(err), time.Time{})
	case status == http.StatusServiceUnavailable || status == 529 || strings.Contains(fmt.Sprint(err), "overloaded_error"):
		accounts.Manager().MarkAccountCooldown(acc.Email, 3*time.Minute)
		accounts.Manager().RecordAccountError(acc.Email, http.StatusServiceUnavailable, fmt.Sprint(err), time.Now().Add(3*time.Minute))
		accounts.GlobalSelector().UnstickAccount(acc.Email)
	}
}
