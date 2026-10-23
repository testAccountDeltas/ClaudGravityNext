// Part of the orchestrate package — see pipeline.go for the package doc
// comment. runStreamingAttempt is a near-literal port of superrelay
// (v1)'s executeStreamingWithWatchdog, rebuilt against reqctx.Params/
// State and internal/stream.FSM instead of a 26-parameter signature and
// the old package-private SSEStreamer. The panic/TTFT watchdog itself now
// lives in watchdog.go (idleWatchdog) rather than being inlined here.
package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/anthropicerr"
	"claudgravity/internal/langdetect"
	"claudgravity/internal/loopguard"
	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/ratelimit"
	"claudgravity/internal/reqctx"
	"claudgravity/internal/requestlog"
	"claudgravity/internal/speccapture"
	"claudgravity/internal/stream"
	"claudgravity/internal/tokens"
	"claudgravity/internal/toolkind"
)

// runStreamingAttempt performs ONE upstream streaming call for p/st
// (already resolved to a specific account/host/payload by retry.go) and
// writes an SSE response to w/flusher. Returns ok=true once the response
// is fully committed to the client (success or a post-headers failure
// already reported as an SSE error block) — the caller can only fail
// over to a different account/host when ok=false.
func runStreamingAttempt(baseCtx context.Context, p *reqctx.Params, st *reqctx.State, accLabel string, finalPayload []byte, w http.ResponseWriter, flusher http.Flusher) (ok bool, status int, retErr error) {
	serverCfg := p.ServerCfg
	panicTimeout := time.Duration(serverCfg.PanicTimeoutSeconds) * time.Second
	idleTimeout := time.Duration(serverCfg.TTFTTimeoutSeconds) * time.Second
	chunkTimeout := StreamingChunkTimeout
	maxStreamDuration := time.Duration(serverCfg.MaxStreamDurationSeconds) * time.Second
	if maxStreamDuration <= 0 {
		maxStreamDuration = 30 * time.Minute
	}

	wd := startIdleWatchdog(baseCtx, p.RID, idleTimeout, chunkTimeout, maxStreamDuration)
	defer wd.Stop()

	upReq, err := http.NewRequestWithContext(wd.ctx, "POST", st.UpstreamURL, bytes.NewReader(finalPayload))
	if err != nil {
		return false, http.StatusInternalServerError, fmt.Errorf("failed to create upstream request: %w", err)
	}
	upReq.Header.Set("Content-Type", "application/json")
	if st.BearerToken != "" {
		upReq.Header.Set("Authorization", "Bearer "+st.BearerToken)
	}
	if p.IsAntigravity {
		upReq.Header.Set("User-Agent", accounts.AntigravityUserAgent)
		upReq.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
		upReq.Header.Set("Client-Metadata", `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`)
	}

	type upResult struct {
		resp *http.Response
		err  error
	}
	resChan := make(chan upResult, 1)
	go func() {
		resp, err := httpClient.Do(upReq)
		resChan <- upResult{resp: resp, err: err}
	}()

	var upResp *http.Response
	resChanConsumed := false
	defer func() {
		if upResp == nil && !resChanConsumed {
			go func() {
				res := <-resChan
				if res.resp != nil && res.resp.Body != nil {
					_ = res.resp.Body.Close()
				}
			}()
		}
	}()

	var wMu sync.Mutex
	var sw *stream.SafeSSEWriter
	earlyHeadersSent := false

	commitHeaders := func() {
		ratelimit.ApplyHeaders(w.Header(), st.ActiveAccount, st.ResolvedModelName)
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		sw = stream.NewSafeSSEWriter(p.RID, w, flusher, speccapture.Global())
		// GAP-S1: the raw w.WriteHeader(200) just above already committed
		// headers on the real socket — sw itself doesn't know that yet
		// (a brand-new writer always starts headersSent=false), so a
		// TerminateWithError called before sw's own first EmitEvent/ping
		// would otherwise wrongly report "headers not sent".
		sw.MarkHeadersAlreadySent()
	}

	t0 := st.StartedAt
	logFailure := func(wasPanicked bool, errStr string, httpStatus int) {
		requestlog.Add(requestlog.RequestLog{
			Timestamp:    t0,
			Key:          p.ClientKey,
			KeyMasked:    requestlog.MaskKey(p.ClientKey),
			Account:      accLabel,
			SessionID:    p.SessionID,
			AgentKey:     p.AgentKey,
			AgentType:    p.AgentType,
			SubagentName: p.SubagentName,
			InstanceID:   p.InstanceID,
			Model:        st.ResolvedModelName,
			TTFT:         time.Since(t0).Milliseconds(),
			TotalDur:     time.Since(t0).Milliseconds(),
			WasPanicked:  wasPanicked,
			Shrunk:       p.WasShrunk,
			Error:        errStr,
			Status:       httpStatus,
		})
	}

	panicTicker := time.NewTicker(panicTimeout)
	defer panicTicker.Stop()

	canFailover := p.IsAntigravity

	select {
	case res := <-resChan:
		resChanConsumed = true
		wd.Touch()
		if res.err != nil {
			if res.resp != nil && res.resp.Body != nil {
				_ = res.resp.Body.Close()
			}
			if errors.Is(res.err, context.Canceled) || errors.Is(baseCtx.Err(), context.Canceled) {
				return false, 499, res.err
			}
			return false, http.StatusBadGateway, res.err
		}
		upResp = res.resp

	case <-wd.ctx.Done():
		if errors.Is(baseCtx.Err(), context.Canceled) {
			return false, 499, baseCtx.Err()
		}
		return false, http.StatusGatewayTimeout, wd.ctx.Err()

	case <-panicTicker.C:
		if canFailover {
			// GAP-DEF-05: When failover is possible, do NOT send early HTTP 200
			// headers before receiving at least one response or headers from Google.
			// Committing headers early locks client and blocks failover.
		waitNoEarlyHeaders:
			for {
				select {
				case res := <-resChan:
					resChanConsumed = true
					wd.Touch()
					if res.err != nil {
						if res.resp != nil && res.resp.Body != nil {
							_ = res.resp.Body.Close()
						}
						if errors.Is(res.err, context.Canceled) || errors.Is(baseCtx.Err(), context.Canceled) {
							return false, 499, res.err
						}
						return false, http.StatusBadGateway, res.err
					}
					upResp = res.resp
					break waitNoEarlyHeaders

				case <-panicTicker.C:
					// Upstream is still working; wait without sending early headers.

				case <-wd.ctx.Done():
					status := http.StatusGatewayTimeout
					if errors.Is(baseCtx.Err(), context.Canceled) {
						status = 499
					}
					logFailure(false, "Upstream context timeout before response", status)
					return false, status, wd.ctx.Err()
				}
			}
		} else {
			wMu.Lock()
			commitHeaders()
			earlyHeadersSent = true
			_ = sw.EmitEvent("ping", `{"type":"ping"}`)
			wMu.Unlock()

		waitAfterPanic:
			for {
				select {
				case res := <-resChan:
					resChanConsumed = true
					wd.Touch()
					if res.err != nil {
						if res.resp != nil && res.resp.Body != nil {
							_ = res.resp.Body.Close()
						}
						wMu.Lock()
						sw.TerminateWithError("overloaded_error", fmt.Sprintf("Upstream timed out or failed: %v", res.err))
						wMu.Unlock()
						logFailure(true, res.err.Error(), http.StatusBadGateway)
						return true, http.StatusBadGateway, res.err
					}
					upResp = res.resp
					break waitAfterPanic

				case <-panicTicker.C:
					wMu.Lock()
					_ = sw.SendKeepalivePing()
					wMu.Unlock()

				case <-wd.ctx.Done():
					wMu.Lock()
					sw.TerminateWithError("overloaded_error", "Upstream timed out: no response received")
					wMu.Unlock()
					logFailure(true, "Upstream context timeout after panic", http.StatusGatewayTimeout)
					return true, http.StatusGatewayTimeout, wd.ctx.Err()
				}
			}
		}
	}

	defer upResp.Body.Close()

	if upResp.StatusCode != http.StatusOK {
		respBytes, _ := io.ReadAll(upResp.Body)
		if !earlyHeadersSent {
			// Headers not sent yet: the caller can still fail over to
			// another account/host for this request.
			return false, upResp.StatusCode, fmt.Errorf("HTTP %d: %s", upResp.StatusCode, string(respBytes))
		}

		wMu.Lock()
		sw.TerminateWithError(anthropicerr.TypeForStatus(upResp.StatusCode), fmt.Sprintf("Upstream HTTP %d: %s", upResp.StatusCode, string(respBytes)))
		wMu.Unlock()
		logFailure(true, fmt.Sprintf("HTTP %d: %s", upResp.StatusCode, string(respBytes)), upResp.StatusCode)
		return true, upResp.StatusCode, fmt.Errorf("HTTP %d: %s", upResp.StatusCode, string(respBytes))
	}

	wMu.Lock()
	if !earlyHeadersSent {
		commitHeaders()
	}
	wMu.Unlock()

	ttft := time.Since(t0)
	claudeBodyBytes := p.RawClaudeBody
	var req claude.Request
	_ = json.Unmarshal(claudeBodyBytes, &req)

	estInputTokens := tokens.WeightedRequestTokens(req)
	if st.EstInputTokens > 0 {
		estInputTokens = st.EstInputTokens
	}
	preferRussian := langdetect.IsRussianConversation(req.Messages)
	respModel := req.Model
	if respModel == "" {
		respModel = st.ResolvedModelName
	}
	hasPrior := hasAssistantTurns(claudeBodyBytes)
	cachePrefixFrac := 0.0
	if hasCacheControlBreakpoint(claudeBodyBytes) {
		cachePrefixFrac = cacheBreakpointPrefixFraction(claudeBodyBytes)
	}
	st.EstInputTokens = estInputTokens
	st.CachePrefixFrac = cachePrefixFrac

	fsmParams := *p
	fsmParams.ModelName = respModel
	fsmParams.PreferRussian = preferRussian
	fsmParams.HasPriorTurns = hasPrior
	fsmParams.DisableParallelToolUse = wantsSingleToolUse(claudeBodyBytes)
	fsmParams.StopSequences = req.StopSequences
	fsm := stream.NewFSM(sw, p.RID, &fsmParams, st)
	fsm.SetChunkRecorder(speccapture.Global())

	err = stream.StreamGeminiToClaude(fsm, upResp.Body, wd.Touch)
	if err != nil {
		wMu.Lock()
		fsm.FinishWithError(err)
		flusher.Flush()
		wMu.Unlock()
	}
	totalDur := time.Since(t0)

	stats := fsm.Stats()
	readCount := 0
	madeProgress := false
	for _, sig := range stats.ToolCallSignatures {
		name, isReadEquiv, isWriteEquiv := loopguard.ReadLoopEquivalentToolName(sig)
		if isReadEquiv {
			readCount++
		} else if toolkind.IsModifyingToolName(name) || isWriteEquiv {
			madeProgress = true
		}
	}
	realProgress := madeProgress
	if readCount == 0 && !madeProgress {
		// GAP-TH-11: a turn with a long chain of reasoning (e.g. 10,000
		// chars of thinking) but a short visible answer was counted as
		// "no progress" toward the read-loop counter, judged on
		// TextChars alone — substantial reasoning is real work, not a
		// stalled read-loop, even when the visible answer is short.
		madeProgress = stats.TextChars > 200 || stats.ThinkingChars > 2000
	}
	loopKey := p.AgentKey + "|" + p.SessionID + "|" + p.InstanceID
	isPlanModeNow := loopguard.IsPlanModeRequest(claudeBodyBytes)
	readWindowSize, _ := loopguard.ReadLoopLimits(p.AgentKey, loopguard.HasModifyingToolsInRequest(claudeBodyBytes))
	loopguard.Global().RecordReadTurnWithRealProgress(loopKey, readCount, madeProgress, realProgress, readWindowSize, isPlanModeNow)

	hasIgnoreTag := loopguard.HasIgnoreLoopTag(stats.LastText) || loopguard.HasIgnoreLoopTag(stats.LastThinking) || loopguard.HasIgnoreLoopTag(string(claudeBodyBytes))
	// Status-check polls repeated while waiting on async work are exempt from oscillation detection.
	oscillationCalls := make([]string, 0, len(stats.ToolCallSignatures))
	for _, sig := range stats.ToolCallSignatures {
		if !loopguard.IsStatusCheckToolCall(sig) {
			oscillationCalls = append(oscillationCalls, sig)
		}
	}
	loopguard.Global().RecordToolCalls(loopKey, oscillationCalls, hasIgnoreTag)

	var hitRate float64
	if stats.InputTokens > 0 {
		hitRate = (float64(stats.CacheTokens) / float64(stats.InputTokens)) * 100.0
	}

	errStr := ""
	reportedStatus := http.StatusOK
	if err != nil {
		errStr = err.Error()
		switch {
		case errors.Is(err, context.Canceled) || errors.Is(baseCtx.Err(), context.Canceled) || strings.Contains(errStr, "context canceled"):
			reportedStatus = 499
		case strings.Contains(errStr, "RESOURCE_EXHAUSTED") || strings.Contains(errStr, "rate_limit_error"):
			reportedStatus = http.StatusTooManyRequests
		case strings.Contains(errStr, "UNAVAILABLE") || strings.Contains(errStr, "overloaded_error"):
			reportedStatus = http.StatusServiceUnavailable
		case strings.Contains(errStr, "DEADLINE_EXCEEDED"):
			// GAP-P0-04: a bare HTTP 500/api_error here reads as fatal to
			// Claude Code's own retry logic — a real Anthropic SDK backs
			// off and retries 504/overloaded_error automatically, and a
			// Google-side timeout on a single upstream call is exactly
			// the kind of transient condition that deserves a retry, not
			// a dropped session.
			reportedStatus = http.StatusGatewayTimeout
		default:
			reportedStatus = http.StatusInternalServerError
		}
	}

	requestlog.Add(requestlog.RequestLog{
		Timestamp:       t0,
		Key:             p.ClientKey,
		KeyMasked:       requestlog.MaskKey(p.ClientKey),
		Account:         accLabel,
		SessionID:       p.SessionID,
		AgentKey:        p.AgentKey,
		AgentType:       p.AgentType,
		SubagentName:    p.SubagentName,
		InstanceID:      p.InstanceID,
		Model:           st.ResolvedModelName,
		TTFT:            ttft.Milliseconds(),
		TotalDur:        totalDur.Milliseconds(),
		InTokens:        stats.InputTokens,
		OutTokens:       stats.OutputTokens,
		CacheReadTokens: stats.CacheTokens,
		CacheHitRate:    hitRate,
		Tools:           stats.ToolNames,
		WasPanicked:     earlyHeadersSent,
		Shrunk:          p.WasShrunk,
		Error:           errStr,
		Status:          reportedStatus,
	})

	return true, reportedStatus, err
}
