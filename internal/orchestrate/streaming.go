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
	"sync"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/anthropicerr"
	"claudgravity/internal/cacheusage"
	"claudgravity/internal/langdetect"
	"claudgravity/internal/logx"
	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/ratelimit"
	"claudgravity/internal/reqctx"
	"claudgravity/internal/requestlog"
	"claudgravity/internal/speccapture"
	"claudgravity/internal/stream"
	"claudgravity/internal/tokens"
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
	chunkTimeout := streamChunkTimeout
	maxStreamDuration := time.Duration(serverCfg.MaxStreamDurationSeconds) * time.Second
	if maxStreamDuration <= 0 {
		maxStreamDuration = defaultMaxStreamDuration
	}

	attemptStart := time.Now()
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
	earlyHeadersSent := false

	// Deferred commit: the HTTP 200 + SSE headers are written lazily, by sw
	// itself, right before its FIRST byte — which is either a keepalive
	// ping (upstream silent for a while) or the first visible content
	// block. Everything before that (message_start, thinking) is held in sw
	// and discarded if the turn turns out bad, so an upstream 429/503, an
	// in-band error, an empty turn, an abnormal finishReason or a truncated
	// stream can still fail over to another account with the client's
	// response untouched. A response already committed by an earlier
	// attempt's ping is reused (tracker), never re-headed.
	tw, tracker := trackCommit(w)
	commitHeaders := func() {
		if tracker.Committed() {
			return
		}
		ratelimit.ApplyHeaders(tw.Header(), st.ActiveAccount, st.ResolvedModelName)
		tw.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		tw.Header().Set("Cache-Control", "no-cache")
		tw.WriteHeader(http.StatusOK)
		flusher.Flush()
	}
	sw := stream.NewSafeSSEWriter(p.RID, tw, flusher, speccapture.Global())
	sw.SetCommitHook(commitHeaders)
	sw.HoldEvents()

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
			if clientGone(baseCtx) {
				return false, 499, res.err
			}
			return false, upstreamErrStatus(res.err), res.err
		}
		upResp = res.resp

	case <-wd.ctx.Done():
		if clientGone(baseCtx) {
			return false, 499, baseCtx.Err()
		}
		return false, http.StatusGatewayTimeout, wd.ctx.Err()

	case <-panicTicker.C:
		if canFailover {
			// GAP-DEF-05: When failover is possible, do NOT send early HTTP 200
			// headers before receiving at least one response or headers from Google.
			// Committing headers early locks client and blocks failover.
			// A silent upstream is not the same as a rejecting one: 429s and
			// 5xx come back fast, so by now the request is hung. Keep the
			// client's connection alive with pings (this is what lets the
			// Claude Code client wait past its own first-byte window) while the
			// attempt stays failover-capable: earlyHeadersSent remains false, so
			// a later error still returns ok=false, and the next attempt reuses
			// the already committed response (see trackCommit above).
			// failoverPingAfter counts from the start of the attempt, and this
			// branch is entered only once panicTimeout has passed.
			untilPing := failoverPingAfter - time.Since(attemptStart)
			if untilPing < time.Millisecond {
				untilPing = time.Millisecond
			}
			hangPing := time.NewTimer(untilPing)
			defer hangPing.Stop()
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
						if clientGone(baseCtx) {
							return false, 499, res.err
						}
						return false, upstreamErrStatus(res.err), res.err
					}
					upResp = res.resp
					break waitNoEarlyHeaders

				case <-panicTicker.C:
					// Upstream is still working; wait without sending early headers.

				case <-hangPing.C:
					wMu.Lock()
					_ = sw.SendKeepalivePing()
					wMu.Unlock()
					hangPing.Reset(failoverPingEvery)

				case <-wd.ctx.Done():
					status := http.StatusGatewayTimeout
					if clientGone(baseCtx) {
						status = 499
					}
					logFailure(false, "Upstream context timeout before response", status)
					return false, status, wd.ctx.Err()
				}
			}
		} else {
			wMu.Lock()
			sw.Commit()
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
						// Same classification as every other branch: a cancellation is
						// the watchdog (504) or the client leaving (499), not a bad
						// gateway. The watchdog's own ctx.Done() case below races this
						// one when both fire together, so they must agree.
						status := upstreamErrStatus(res.err)
						if clientGone(baseCtx) {
							status = 499
						}
						wMu.Lock()
						sw.TerminateWithError("overloaded_error", fmt.Sprintf("Upstream timed out or failed: %v", res.err))
						wMu.Unlock()
						logFailure(true, res.err.Error(), status)
						return true, status, res.err
					}
					upResp = res.resp
					break waitAfterPanic

				case <-panicTicker.C:
					wMu.Lock()
					_ = sw.SendKeepalivePing()
					wMu.Unlock()

				case <-wd.ctx.Done():
					status := http.StatusGatewayTimeout
					if clientGone(baseCtx) {
						status = 499 // the client left, the watchdog did not fire
					}
					wMu.Lock()
					sw.TerminateWithError("overloaded_error", "Upstream timed out: no response received")
					wMu.Unlock()
					logFailure(true, "Upstream context timeout after panic", status)
					return true, status, wd.ctx.Err()
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
	cacheInfo := cacheusage.Analyze(claudeBodyBytes)
	cachePrefixFrac := cacheInfo.PrefixFrac
	st.CacheFrac1h = cacheInfo.Frac1h
	st.EstInputTokens = estInputTokens
	st.CachePrefixFrac = cachePrefixFrac

	fsmParams := *p
	fsmParams.ModelName = respModel
	fsmParams.PreferRussian = preferRussian
	fsmParams.HasPriorTurns = hasPrior
	fsmParams.DisableParallelToolUse = wantsSingleToolUse(claudeBodyBytes)
	fsmParams.StopSequences = req.StopSequences
	fsmParams.MaxTokens = req.MaxTokens
	fsm := stream.NewFSM(sw, p.RID, &fsmParams, st)
	fsm.SetChunkRecorder(speccapture.Global())

	err = stream.StreamGeminiToClaude(fsm, upResp.Body, wd.Touch)
	turnErr := asTurnError(err)
	if err != nil && !sw.IsReleased() {
		// Nothing of this turn reached the client (events still held):
		// drop them and let the caller retry on another account/host.
		sw.DiscardHeld()
		status := http.StatusBadGateway
		switch {
		case turnErr != nil:
			status = turnErr.Status
		case clientGone(baseCtx):
			status = 499
		case errors.Is(err, context.Canceled):
			status = http.StatusGatewayTimeout // idle watchdog, not a client disconnect
		}
		fs := fsm.Stats()
		logx.LogGateway("[#%d] << turn failed before any content (retryable): finishReason=%q status=%d err=%v usage in=%d out=%d cache_read=%d",
			p.RID, fs.FinishReason, status, err, fs.InputTokens, fs.OutputTokens, fs.CacheTokens)
		logFailure(false, err.Error(), status)
		return false, status, err
	}
	if err != nil && turnErr == nil {
		wMu.Lock()
		fsm.FinishWithError(err)
		flusher.Flush()
		wMu.Unlock()
	}
	totalDur := time.Since(t0)

	stats := fsm.Stats()
	logx.LogGateway("[#%d] << finishReason=%q stop_reason=%s usage in=%d out=%d cache_read=%d tools=%v err=%v",
		p.RID, stats.FinishReason, stats.StopReason, stats.InputTokens, stats.OutputTokens, stats.CacheTokens, stats.ToolNames, err)
	recordStreamLoopGuard(p, stats, claudeBodyBytes)

	var hitRate float64
	if stats.InputTokens > 0 {
		hitRate = (float64(stats.CacheTokens) / float64(stats.InputTokens)) * 100.0
	}

	errStr := ""
	reportedStatus := http.StatusOK
	if err != nil {
		errStr = err.Error()
		reportedStatus = reportedStreamStatus(baseCtx, err, turnErr)
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
