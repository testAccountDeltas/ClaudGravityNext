// Package orchestrate is a port of superrelay (v1)'s server.go retry/
// failover/watchdog pipeline (executeStreamingWithWatchdog,
// executeNonStreaming, and the account-rotation + host-fallback +
// session-bump retry loop that drove both from handleMessages) — the
// "single conveyor belt" ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md
// Part 4.3 calls internal/orchestrate/pipeline.go: Execute() runs ONE
// incoming HTTP transaction's whole retry/failover loop across accounts,
// delegating one attempt at a time to streaming.go/nonstreaming.go, with
// watchdog.go's idleWatchdog and retry.go's bump/classification policy as
// reusable pieces instead of being inlined.
//
// Scope boundary (deliberate, not an oversight): internal/httpapi (not
// yet built) owns request parsing/validation, tenant resolution, the
// fast-path intercepts (direct web-search, the 1-token quota probe), and
// calling internal/translate exactly once to produce the FIRST attempt's
// reqctx.State — none of that is retry/failover POLICY, so none of it
// belongs here. Execute receives that already-translated initial State
// and owns everything from "pick an account" onward.
//
// Orchestrator is deliberately a zero-field struct for now: every
// dependency it would hold (the account pool/selector, the request log,
// spec capture) is already a package-level singleton with its own
// exported accessor elsewhere in this same migration (accounts.Manager/
// accounts.GlobalSelector, requestlog.Add, speccapture.Global) — giving
// Orchestrator its own copies of those same singletons as struct fields
// would be dependency injection in name only, not in effect, so this
// follows the precedent already set throughout Phases 1-3 rather than
// introducing DI for the first time in this one package.
package orchestrate

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts"
	"claudgravity/internal/logx"
	"claudgravity/internal/ratelimit"
	"claudgravity/internal/reqctx"
	"claudgravity/internal/warmup"
)

// maxInlineDataBytes mirrors internal/httpapi's own pre-check (on the
// RAW Claude request) — this second check runs on the FINAL wrapped
// Antigravity/public-API payload, which a long conversation history's
// own JSON (thousands of turns, no single image over the per-block
// limit) plus wrapping overhead can still push over the limit even when
// every individual image passed the first check.
const maxInlineDataBytes = 20 * 1024 * 1024

// Orchestrator runs one incoming HTTP transaction's full retry/failover
// pipeline. See the package doc comment for why it currently holds no
// fields.
type Orchestrator struct{}

// Execute is superrelay (v1)'s handleMessages retry loop (section "4.
// Pre-stream Failover Loop"), rebuilt against reqctx.Params/State. p is
// immutable for the whole request; st is the FIRST attempt's seed,
// produced by the caller from internal/translate's Result — Execute
// rebuilds a fresh *reqctx.State for every subsequent account-rotation
// attempt (never reuses or mutates one across attempts), per reqctx's own
// documented contract.
//
// ok=true means a response has ALREADY been fully written to w (success,
// or a post-headers failure already reported as an SSE error block) —
// the caller must not write anything more. ok=false means nothing was
// ever written; the caller writes status/err itself (e.g. via
// anthropicerr.TypeForStatus), matching the exact GAP-D1-01 boundary
// internal/stream.SafeSSEWriter.TerminateWithError already enforces one
// layer down.
func (o *Orchestrator) Execute(ctx context.Context, p *reqctx.Params, st *reqctx.State, w http.ResponseWriter) (ok bool, httpStatus int, retErr error) {
	flusher, hasFlusher := w.(http.Flusher)
	isStreaming := p.IsStreaming && hasFlusher

	convAnchor := p.ConvAnchor
	if convAnchor == "" {
		convAnchor = accounts.DeriveConversationAnchor(p.RawClaudeBody, p.SessionID, p.InstanceID)
	}

	excludedAccounts := make(map[string]bool)
	var lastErr error
	lastStatus := http.StatusBadGateway
	success := false

	baseModelName := st.ResolvedModelName

	for attempt := 0; attempt < maxAttempts; attempt++ {
		attemptState := &reqctx.State{
			Attempt:           attempt,
			StartedAt:         st.StartedAt,
			ResolvedModelName: baseModelName,
			GeminiBody:        st.GeminiBody,
			EstInputTokens:    st.EstInputTokens,
			CachePrefixFrac:   st.CachePrefixFrac,
		}

		var accLabel string
		projectID := "aicode-consumers"

		if !p.IsAntigravity {
			accLabel = "DirectAPIKey"
		} else {
			acc, err := accounts.GlobalSelector().SelectAccountExcluding(p.ClientKey, convAnchor, p.Tenant, excludedAccounts)
			if err != nil {
				// The whole pool is on cooldown (every account quota-
				// exhausted) — a rate-limit condition, not a gateway
				// failure. Claude Code treats 502 as a fatal connection
				// error and aborts the session instead of running its
				// normal 429/unified-reset countdown.
				lastErr = err
				lastStatus = http.StatusTooManyRequests
				break
			}
			attemptState.ActiveAccount = acc
			tok, err := accounts.Manager().RefreshAccessToken(acc.Email)
			if err != nil {
				logx.LogGateway("[#%d] !! Token refresh failed for %s: %v (rotating account)", p.RID, acc.Email, err)
				excludedAccounts[acc.Email] = true
				lastErr = err
				continue
			}
			attemptState.BearerToken = tok
			accLabel = acc.Email
			if acc.ProjectID != "" {
				projectID = acc.ProjectID
			}

			if (baseModelName == "claude-sonnet-4-6" || baseModelName == "claude-opus-4-6-thinking" || baseModelName == "gpt-oss-120b-medium") && ratelimit.IsNative3PModelExhausted(attemptState.ActiveAccount) {
				logx.LogGateway("[#%d] !! Native 3P model %s has exhausted quota on selected account %s, remapping to gemini-3.8-flash-tiered", p.RID, baseModelName, acc.Email)
				attemptState.ResolvedModelName = "gemini-3.8-flash-tiered"
			}
		}

		var directURL string
		if !p.IsAntigravity {
			modelName := attemptState.ResolvedModelName
			if strings.HasPrefix(modelName, "gemini-3.8-flash") {
				// tiering's model names are Antigravity/CloudCode-
				// internal aliases the public Gemini Developer API
				// doesn't recognize.
				modelName = "gemini-2.5-flash"
				attemptState.ResolvedModelName = modelName
			}
			baseURL := p.Tenant.GoogleBaseURL
			if baseURL == "" {
				baseURL = "https://generativelanguage.googleapis.com"
			}
			if isStreaming {
				directURL = fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse", baseURL, modelName)
			} else {
				directURL = fmt.Sprintf("%s/v1beta/models/%s:generateContent", baseURL, modelName)
			}
			if p.Tenant.GoogleAPIKey != "" {
				if strings.Contains(directURL, "?") {
					directURL += "&key=" + p.Tenant.GoogleAPIKey
				} else {
					directURL += "?key=" + p.Tenant.GoogleAPIKey
				}
			}
		}

		antigravitySessionKey := p.SessionID
		if p.InstanceID != "" {
			antigravitySessionKey = p.SessionID + ":" + p.InstanceID
		} else if p.AgentType == "auxiliary" {
			// auxiliary (title/summary generation) and the main thread
			// both carry InstanceID=="" — without this they'd collapse
			// onto the exact same session bucket, shared between two
			// completely different models and contexts.
			antigravitySessionKey = p.SessionID + ":aux"
		}
		bump := getSessionBump(antigravitySessionKey)
		bumpsThisAttempt := 0
		hostIdx := 0
		locationGatePasses := 0
		success = false

		for {
			if p.IsAntigravity {
				host := warmup.MainInferenceHosts[hostIdx]
				if isStreaming {
					attemptState.UpstreamURL = host + "/v1internal:streamGenerateContent?alt=sse"
				} else {
					attemptState.UpstreamURL = host + "/v1internal:generateContent"
				}
			} else {
				attemptState.UpstreamURL = directURL
			}

			var finalPayload []byte
			if p.IsAntigravity {
				finalPayload = warmup.WrapAntigravityPayload(attemptState.ResolvedModelName, st.GeminiBody, projectID, bump, antigravitySessionKey)
			} else {
				finalPayload = stripThoughtFieldsForPublicAPI(st.GeminiBody)
			}

			if len(finalPayload) > maxInlineDataBytes {
				return false, http.StatusRequestEntityTooLarge, fmt.Errorf("request body exceeds the 20MB upstream limit")
			}

			logx.LogGateway("[#%d] >> POST /v1/messages [attempt %d/%d, session-gen %d] account=%s model=%s stream=%v bytes=%d [%s]",
				p.RID, attempt+1, maxAttempts, bump, accLabel, attemptState.ResolvedModelName, isStreaming, len(finalPayload), p.AgentKey)

			if isStreaming {
				success, lastStatus, lastErr = runStreamingAttempt(ctx, p, attemptState, accLabel, finalPayload, w, flusher)
			} else {
				success, lastStatus, lastErr = runNonStreamingAttempt(ctx, p, attemptState, accLabel, finalPayload, w)
			}

			if success || !p.IsAntigravity {
				break
			}
			if ctx.Err() != nil {
				break
			}
			errText := fmt.Sprint(lastErr)

			if warmup.IsAntigravityLocationGateError(lastStatus, errText) || warmup.IsTransientNetworkError(lastStatus, errText) {
				hostIdx++
				if hostIdx >= len(warmup.MainInferenceHosts) {
					hostIdx = 0
					locationGatePasses++
				}
				if locationGatePasses >= maxLocationGatePasses {
					logx.LogGateway("[#%d] !! Location/network gate exhausted all hosts %d time(s) on %s, giving up on this account", p.RID, locationGatePasses, accLabel)
					if warmup.IsTransientNetworkError(lastStatus, errText) {
						return false, lastStatus, lastErr
					}
					break
				}
				logx.LogGateway("[#%d] !! Location/network error on %s, retrying on next host: %s", p.RID, accLabel, errText)
				continue
			}

			if warmup.IsSessionTokenWallError(lastStatus, errText) {
				if bumpsThisAttempt >= maxBumpsPerAttempt {
					break
				}
				bumpsThisAttempt++
				bump++
				setSessionBump(antigravitySessionKey, bump)
				logx.LogGateway("[#%d] !! Session token-wall hit on %s, bumping to a fresh session bucket (gen %d) and retrying same account", p.RID, accLabel, bump)
				continue
			}

			break
		}

		if success {
			// success==true here only means "the response was already
			// committed to the client" — the panic watchdog can send an
			// early 200 + SSE headers and the upstream call can still go
			// on to fail (429/403) afterwards. lastStatus is the real
			// verdict.
			if lastStatus == http.StatusOK {
				if attemptState.ActiveAccount != nil && convAnchor != "" {
					accounts.GlobalSelector().RememberAffinity(convAnchor, attemptState.ActiveAccount.Email)
				}
			} else if attemptState.ActiveAccount != nil {
				applyPostFailureCooldown(attemptState.ActiveAccount, lastStatus, lastErr)
			}
			return true, lastStatus, nil
		}

		// The client disconnecting cancels ctx, which is exactly why the
		// upstream call just failed — not a verdict on the account at
		// all. Retrying on a DIFFERENT account for a request nobody is
		// listening for anymore just cycles the rest of the pool for no
		// reason before giving up once maxAttempts runs out.
		if ctx.Err() != nil {
			logx.LogGateway("[#%d] Client disconnected (%v), aborting retry loop without touching account state", p.RID, ctx.Err())
			return false, 499, ctx.Err()
		}

		// Client-side 400 Bad Request handling: if it's a thoughtSignature mismatch,
		// self-heal the payload with skip_thought_signature_validator and retry on the
		// same/healthy account without burning or excluding the account (which would break
		// single-account setups). Any other 400 is non-retryable.
		if lastStatus == http.StatusBadRequest {
			if p.IsAntigravity && isThoughtSignatureMismatchError(lastStatus, lastErr) {
				logx.LogGateway("[#%d] Upstream returned 400 Bad Request on thoughtSignature mismatch, auto-healing with skip_thought_signature_validator...", p.RID)
				st.GeminiBody = replaceThoughtSignaturesWithSkipValidator(st.GeminiBody)
				time.Sleep(50 * time.Millisecond)
				continue
			}
			if !warmup.IsAntigravityLocationGateError(lastStatus, fmt.Sprint(lastErr)) {
				logx.LogGateway("[#%d] Upstream returned 400 Bad Request (non-retryable client/schema error), aborting failover: %v", p.RID, lastErr)
				break
			}
			// LocationGate error on this account: proceed to exclude account and failover to next account.
		}

		if attemptState.ActiveAccount != nil {
			excludedAccounts[attemptState.ActiveAccount.Email] = true
			applyPostFailureCooldown(attemptState.ActiveAccount, lastStatus, lastErr)
		}

		if !p.IsAntigravity {
			break // No multiple accounts to rotate for a direct API key.
		}

		logx.LogGateway("[#%d] !! Pre-stream failover: attempt %d failed (status=%d, err=%v), retrying on next account...", p.RID, attempt+1, lastStatus, lastErr)
		time.Sleep(100 * time.Millisecond) // brief jitter before retry
	}

	if !success && lastErr != nil {
		logx.LogGateway("[#%d] !! All pre-stream failover attempts failed: %v", p.RID, lastErr)
		return false, lastStatus, fmt.Errorf("upstream failed after retries: %w", lastErr)
	}
	return success, lastStatus, lastErr
}

// applyPostFailureCooldown mirrors v1's identical cooldown/unstick/
// invalidate-token bookkeeping that ran at BOTH of its two call sites
// (the early-headers-sent branch inside "if success" and the pre-headers
// failure branch below it) — kept as one function so the two call sites
// in Execute above can never drift out of sync with each other again,
// the exact failure mode a Round 15 v1 audit caught between them.
func applyPostFailureCooldown(acc *accounts.Record, status int, err error) {
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
		accounts.Manager().MarkAccountCooldown(acc.Email, cooldown)
		accounts.Manager().RecordAccountError(acc.Email, http.StatusTooManyRequests, fmt.Sprint(err), time.Now().Add(cooldown))
		// GAP-ACC-01: without this, the session's sticky-account
		// binding stayed pointed at an account that just got a 15-minute
		// cooldown — a retry later in the SAME session (or a request
		// sent moments later from an already-bound client) could get
		// stuck selecting the same cooling-down account again instead
		// of a healthy one.
		accounts.GlobalSelector().UnstickAccount(acc.Email)
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
