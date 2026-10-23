// Package httpapi is the HTTP-layer port of superrelay (v1)'s server.go:
// routing, request parsing/validation, tenant resolution, and the thin
// per-endpoint handlers that call into internal/translate and
// internal/orchestrate for the actual work. Per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 4.3, this
// package is exactly 8 files: router.go, messages.go, counttokens.go,
// models.go, usage.go, errors.go, middleware.go, legacy.go.
//
// Deliberately NOT in this package (confirmed against the spec's own
// httpapi file list, which names none of these): the large pile of
// Claude-Code CLI control-endpoint spoofing stub closures that lived
// inline in v1's main() (/api/claude_cli_profile, /api/oauth/validate,
// /api/claude_code/organizations/..., /api/event_logging, /api/telemetry,
// /managed/settings, billing/policy_limits/notification_preferences/etc),
// handleEmbeddings, and handleOAuthUsage. These now live in
// internal/clistubs (GAP-P0-02: they were missing entirely — a 401 from
// dashboard's catch-all "/" or a 404 from this package's own /v1/
// catch-all — not just deferred), registered on the same top-level mux
// as this package's router, in cmd/gateway/main.go, before dashboard's
// "/". Also not in this package:
// cmd/gateway/main.go's actual server construction (TLS/CONNECT listener
// wiring, graceful shutdown, startup account/model-catalog loading) —
// router.go here only builds the http.Handler; wiring it into a running
// process is cmd/gateway's job.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// setSplitTokenRateLimitHeaders sets the input/output-specific token
// rate-limit headers alongside the aggregated anthropic-ratelimit-tokens-*
// triplet writeAnthropicError already sets on a 429/529. Kept as this
// package's own small copy rather than importing internal/ratelimit's
// identical-but-private helper — the same single-small-helper-duplication
// precedent internal/orchestrate's signals.go already set for
// stripThoughtFieldsForPublicAPI rather than exporting across a package
// boundary for one three-line function.
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

// WriteAnthropicError writes an error body matching Anthropic's actual
// wire format ({"type":"error","error":{"type":...,"message":...}}) with
// Content-Type: application/json, plus every 429/529-specific header a
// real Anthropic SDK or Claude Code's own backoff/countdown display reads
// (Retry-After, anthropic-ratelimit-unified-reset, the standard
// requests/tokens limit-remaining-reset triplets) — a direct, faithful
// port of v1's writeAnthropicError, since every header here was added in
// response to a specific confirmed client-side failure mode (an SDK that
// hard-fails instead of backing off, Claude Code's countdown having
// nothing to count down to, a strict client dividing by a missing *-limit
// header).
func WriteAnthropicError(w http.ResponseWriter, status int, errType, message string) {
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("anthropic-version", "2023-06-01")
	h.Set("anthropic-organization-id", fakeOrgID)
	h.Set("anthropic-workspace-id", fakeWorkspaceID)
	rid := h.Get("request-id")
	if rid == "" {
		rid = h.Get("x-request-id")
	}
	if rid == "" {
		rid = nextRequestID()
	}
	h.Set("request-id", rid)
	h.Set("x-request-id", rid)

	// The real Anthropic API answers an overloaded backend with HTTP 529,
	// not 502/503/504 — some SDK retry/backoff logic keys off the literal
	// wire status code, not just the JSON "type" field.
	wireStatus := status
	if errType == "overloaded_error" {
		wireStatus = 529
	}

	if status == http.StatusTooManyRequests {
		// 900s matches MarkAccountCooldown's own 15-minute cooldown window
		// for a 429/RESOURCE_EXHAUSTED account elsewhere in this gateway —
		// a client honoring Retry-After literally that retries in 5s (the
		// old value) hits the exact same cooldown again immediately.
		h.Set("Retry-After", "900")
		if h.Get("anthropic-ratelimit-unified-reset") == "" {
			h.Set("anthropic-ratelimit-unified-reset", fmt.Sprintf("%d", time.Now().Add(15*time.Minute).Unix()))
			h.Set("anthropic-ratelimit-unified-status", "rejected")
		}
		if h.Get("anthropic-ratelimit-unified-representative-claim") == "" {
			h.Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
		}
		if h.Get("anthropic-ratelimit-unified-overage-status") == "" {
			h.Set("anthropic-ratelimit-unified-overage-status", "rejected")
		}
		if h.Get("anthropic-ratelimit-unified-overage-disabled-reason") == "" {
			h.Set("anthropic-ratelimit-unified-overage-disabled-reason", "org_level_disabled")
		}
		// GAP-D1-12: without these, Claude Code CLI's own countdown
		// display in the status line has nothing to count down to on a
		// 429 — matches the same unified-5h/7d header set ratelimit.go
		// already sends on a normal (non-error) response.
		if h.Get("anthropic-ratelimit-unified-5h-status") == "" {
			h.Set("anthropic-ratelimit-unified-5h-status", "rejected")
			h.Set("anthropic-ratelimit-unified-5h-utilization", "1.0")
			h.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", time.Now().Add(15*time.Minute).Unix()))
		}
		if h.Get("anthropic-ratelimit-unified-7d-status") == "" {
			h.Set("anthropic-ratelimit-unified-7d-status", "rejected")
			h.Set("anthropic-ratelimit-unified-7d-utilization", "1.0")
			h.Set("anthropic-ratelimit-unified-7d-reset", fmt.Sprintf("%d", time.Now().Add(15*time.Minute).Unix()))
		}
		h.Set("anthropic-ratelimit-requests-remaining", "0")
		h.Set("anthropic-ratelimit-tokens-remaining", "0")
		if h.Get("anthropic-ratelimit-requests-limit") == "" {
			h.Set("anthropic-ratelimit-requests-limit", "1000")
		}
		tokLimit := 1000000
		if limitStr := h.Get("anthropic-ratelimit-tokens-limit"); limitStr != "" {
			if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
				tokLimit = n
			}
		} else {
			h.Set("anthropic-ratelimit-tokens-limit", "1000000")
		}
		setSplitTokenRateLimitHeaders(h, tokLimit, 0, time.Now().Add(15*time.Minute))
		resetAt := time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339)
		if h.Get("anthropic-ratelimit-requests-reset") == "" {
			h.Set("anthropic-ratelimit-requests-reset", resetAt)
		}
		if h.Get("anthropic-ratelimit-tokens-reset") == "" {
			h.Set("anthropic-ratelimit-tokens-reset", resetAt)
		}
	} else if errType == "overloaded_error" {
		h.Set("Retry-After", "3")
		if h.Get("anthropic-ratelimit-unified-reset") == "" {
			h.Set("anthropic-ratelimit-unified-reset", fmt.Sprintf("%d", time.Now().Add(30*time.Second).Unix()))
			h.Set("anthropic-ratelimit-unified-status", "rejected")
		}
		if h.Get("anthropic-ratelimit-unified-representative-claim") == "" {
			h.Set("anthropic-ratelimit-unified-representative-claim", "five_hour")
		}
		if h.Get("anthropic-ratelimit-unified-overage-status") == "" {
			h.Set("anthropic-ratelimit-unified-overage-status", "rejected")
		}
		if h.Get("anthropic-ratelimit-unified-overage-disabled-reason") == "" {
			h.Set("anthropic-ratelimit-unified-overage-disabled-reason", "org_level_disabled")
		}
		if h.Get("anthropic-ratelimit-unified-5h-status") == "" {
			h.Set("anthropic-ratelimit-unified-5h-status", "rejected")
			h.Set("anthropic-ratelimit-unified-5h-utilization", "1.0")
			h.Set("anthropic-ratelimit-unified-5h-reset", fmt.Sprintf("%d", time.Now().Add(30*time.Second).Unix()))
		}
		if h.Get("anthropic-ratelimit-unified-7d-status") == "" {
			h.Set("anthropic-ratelimit-unified-7d-status", "rejected")
			h.Set("anthropic-ratelimit-unified-7d-utilization", "1.0")
			h.Set("anthropic-ratelimit-unified-7d-reset", fmt.Sprintf("%d", time.Now().Add(30*time.Second).Unix()))
		}
		if h.Get("anthropic-ratelimit-requests-limit") == "" {
			h.Set("anthropic-ratelimit-requests-limit", "1000")
		}
		tokLimit529 := 1000000
		if limitStr := h.Get("anthropic-ratelimit-tokens-limit"); limitStr != "" {
			if n, err := strconv.Atoi(limitStr); err == nil && n > 0 {
				tokLimit529 = n
			}
		} else {
			h.Set("anthropic-ratelimit-tokens-limit", "1000000")
		}
		setSplitTokenRateLimitHeaders(h, tokLimit529, 0, time.Now().Add(30*time.Second))
		h.Set("anthropic-ratelimit-requests-remaining", "0")
		h.Set("anthropic-ratelimit-tokens-remaining", "0")
		resetAt529 := time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339)
		if h.Get("anthropic-ratelimit-requests-reset") == "" {
			h.Set("anthropic-ratelimit-requests-reset", resetAt529)
		}
		if h.Get("anthropic-ratelimit-tokens-reset") == "" {
			h.Set("anthropic-ratelimit-tokens-reset", resetAt529)
		}
	}

	w.WriteHeader(wireStatus)
	body, _ := json.Marshal(map[string]interface{}{
		"type": "error",
		"error": map[string]string{
			"type":    errType,
			"message": message,
		},
	})
	_, _ = w.Write(body)
}
