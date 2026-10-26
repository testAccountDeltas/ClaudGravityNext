// Part of the orchestrate package — see pipeline.go for the package doc
// comment. runNonStreamingAttempt is a near-literal port of superrelay
// (v1)'s executeNonStreaming, rebuilt against reqctx.Params/State.
package orchestrate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts"
	"claudgravity/internal/cacheusage"
	"claudgravity/internal/logx"
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

// runNonStreamingAttempt performs ONE upstream non-streaming call for
// p/st and writes a complete Anthropic Messages API JSON response to w.
func runNonStreamingAttempt(baseCtx context.Context, p *reqctx.Params, st *reqctx.State, accLabel string, finalPayload []byte, w http.ResponseWriter) (ok bool, status int, retErr error) {
	serverCfg := p.ServerCfg
	timeout := time.Duration(serverCfg.TTFTTimeoutSeconds+serverCfg.ReadTimeoutSeconds) * time.Second
	ctx, cancel := context.WithTimeout(baseCtx, timeout)
	defer cancel()

	upReq, err := http.NewRequestWithContext(ctx, "POST", st.UpstreamURL, bytes.NewReader(finalPayload))
	if err != nil {
		return false, http.StatusInternalServerError, fmt.Errorf("failed to create upstream request: %w", err)
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Accept", "application/json")
	if st.BearerToken != "" {
		upReq.Header.Set("Authorization", "Bearer "+st.BearerToken)
	}
	if p.IsAntigravity {
		upReq.Header.Set("User-Agent", accounts.AntigravityUserAgent)
	}

	logFailure := func(status int, errStr string) {
		durMs := time.Since(st.StartedAt).Milliseconds()
		requestlog.Add(requestlog.RequestLog{
			Timestamp:    st.StartedAt,
			Key:          p.ClientKey,
			KeyMasked:    requestlog.MaskKey(p.ClientKey),
			Account:      accLabel,
			SessionID:    p.SessionID,
			AgentKey:     p.AgentKey,
			AgentType:    p.AgentType,
			SubagentName: p.SubagentName,
			InstanceID:   p.InstanceID,
			Model:        st.ResolvedModelName,
			TTFT:         durMs,
			TotalDur:     durMs,
			WasPanicked:  false,
			Shrunk:       p.WasShrunk,
			Error:        errStr,
			Status:       status,
		})
	}

	resp, err := httpClient.Do(upReq)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.Canceled) || errors.Is(baseCtx.Err(), context.Canceled) {
			status = 499
		}
		logFailure(status, err.Error())
		return false, status, err
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.Canceled) || errors.Is(baseCtx.Err(), context.Canceled) {
			status = 499
		}
		logFailure(status, err.Error())
		return false, status, fmt.Errorf("failed to read upstream body: %w", err)
	}
	speccapture.Global().RecordGeminiResponse(p.RID, resp.Header, respBytes)

	if resp.StatusCode != http.StatusOK {
		errStr := fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(respBytes))
		logFailure(resp.StatusCode, errStr)
		return false, resp.StatusCode, errors.New(errStr)
	}

	claudeBodyBytes := p.RawClaudeBody
	var req claude.Request
	_ = json.Unmarshal(claudeBodyBytes, &req)

	geminiJSON := gjson.ParseBytes(respBytes)
	if respNode := geminiJSON.Get("response"); respNode.Exists() {
		geminiJSON = respNode
	}
	if errNode := geminiJSON.Get("error"); errNode.Exists() {
		msg := errNode.Get("message").String()
		errCode := int(errNode.Get("code").Int())
		if errCode == 0 {
			errCode = http.StatusInternalServerError
		}
		// Caller's own anthropicerr.TypeForStatus(errCode) reconstructs
		// the right classification from the numeric code alone — do not
		// write a response body here (the ONLY `return false` in this
		// function that used to do so, which the caller's own retry/
		// final-error-write paths could then double-write on top of).
		return false, errCode, fmt.Errorf("upstream error: %s", msg)
	}
	if pf := geminiJSON.Get("promptFeedback"); pf.Exists() {
		if br := pf.Get("blockReason").String(); br != "" && br != "BLOCK_REASON_UNSPECIFIED" {
			return writeBlockedResponse(p, st, accLabel, claudeBodyBytes, br, w)
		}
	}

	inTok := geminiJSON.Get("usageMetadata.promptTokenCount").Int()
	// Single point of application for the non-streaming path's live
	// driftRatio measurement (Part 6.4/Part 11 DoD #4) — mirrors
	// internal/stream/chunk.go's streaming-path hook: log the real
	// upstream value against this request's own pre-flight estimate
	// BEFORE inTok is possibly overwritten by that same estimate as a
	// fallback below, so the logged ratio always compares two
	// independently-derived numbers, never an estimate against itself.
	estimatedInTok := tokens.WeightedRequestTokens(req)
	if st.EstInputTokens > 0 {
		estimatedInTok = st.EstInputTokens
	}
	tokens.LogDriftRatio(p.RID, estimatedInTok, inTok)
	if inTok == 0 {
		inTok = int64(estimatedInTok)
	}
	thinkingTok := geminiJSON.Get("usageMetadata.thoughtsTokenCount").Int()
	outTok := geminiJSON.Get("usageMetadata.candidatesTokenCount").Int() + thinkingTok
	cacheTok := geminiJSON.Get("usageMetadata.cachedContentTokenCount").Int()

	uncachedInTok := inTok - cacheTok
	if uncachedInTok < 0 {
		uncachedInTok = 0
	}
	// Cache write accounting never invents numbers (see cacheusage.Split):
	// only when a breakpoint exists, upstream read nothing from cache and
	// the prefix reaches the model's documented minimum.
	cacheInfo := cacheusage.Analyze(claudeBodyBytes)
	cc5m, cc1h := cacheusage.Split(req.Model, uncachedInTok, cacheTok, cacheInfo.PrefixFrac, cacheInfo.Frac1h)
	cacheCreationTok := cc5m + cc1h
	uncachedInTok -= cacheCreationTok

	cwdKey := p.SessionID
	if p.InstanceID != "" {
		cwdKey = p.SessionID + ":" + p.InstanceID
	}

	if inTok > 0 {
		if cacheTok > 0 {
			stream.SetSessionCacheFraction(p.SessionID, float64(cacheTok)/float64(inTok))
		} else if cacheCreationTok > 0 {
			stream.SetSessionCacheFraction(p.SessionID, 0.85)
		} else {
			stream.SetSessionCacheFraction(p.SessionID, 0.0)
		}
	}

	usageMap := map[string]interface{}{
		"input_tokens":                uncachedInTok,
		"output_tokens":               outTok,
		"cache_creation_input_tokens": cacheCreationTok,
		"cache_creation":              cacheusage.Object(cc5m, cc1h),
		"cache_read_input_tokens":     cacheTok,
	}
	// GAP-TH-07
	if thinkingTok > 0 {
		usageMap["output_tokens_details"] = map[string]interface{}{
			"thinking_tokens": thinkingTok,
		}
	}

	finishReason := geminiJSON.Get("candidates.0.finishReason").String()
	finishClass := stream.ClassifyFinishReason(finishReason)
	stopReason := finishClass.StopReason
	var stopDetails map[string]interface{}
	if stopReason == "refusal" {
		stopDetails = map[string]interface{}{
			"type":        "refusal",
			"category":    "general_harms",
			"explanation": "Content blocked by safety policy (finishReason=" + finishReason + ")",
		}
	}

	respModel := req.Model
	if respModel == "" {
		respModel = st.ResolvedModelName
	}
	claudeResp := map[string]interface{}{
		"id":            fmt.Sprintf("msg_%d", time.Now().UnixNano()),
		"type":          "message",
		"role":          "assistant",
		"model":         respModel,
		"content":       []map[string]interface{}{},
		"usage":         usageMap,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"stop_details":  stopDetails,
	}

	if p.ContextManagement != nil {
		claudeResp["context_management"] = p.ContextManagement
	}

	cand := geminiJSON.Get("candidates.0")
	cr := processCandidate(p, cand, claudeResp, claudeBodyBytes, req, cwdKey)
	toolsCalled, toolCallIDs, toolCallSignatures := cr.toolsCalled, cr.toolCallIDs, cr.toolCallSignatures
	textChars, thinkingChars, lastText, lastThinking := cr.textChars, cr.thinkingChars, cr.lastText, cr.lastThinking

	// A bad turn (abnormal finishReason with no usable tool call, or an
	// empty turn) is NOT a response: nothing has been written yet, so
	// report it as a retryable error instead of a 200 end_turn.
	hasToolUse := len(cr.toolCallIDs) > 0
	prefillOnly := p.PrefillText != "" && !hasToolUse && textChars == 0 &&
		finishClass.StopReason == "end_turn" && !finishClass.ToolFailure
	if finishClass.StopReason == "max_tokens" && !st.OutputAllowanceBumped && textChars == 0 && !hasToolUse &&
		(thinkingTok > 0 || thinkingChars > 0) {
		// Output limit hit while thinking, nothing visible: retried once
		// with a widened allowance (pipeline.go), then reported as is.
		terr := &stream.UpstreamTurnError{ErrType: "overloaded_error", Status: http.StatusServiceUnavailable,
			FinishReason: finishReason, NeedsMoreOutput: true,
			Msg: "upstream hit the output token limit while thinking, before any visible output"}
		logFailure(terr.Status, terr.Error())
		return false, terr.Status, terr
	}
	// A turn whose only product was a call the request forbids is finished, not
	// "empty": retrying it would just repeat the same call (three upstream
	// round-trips for a label), so the client gets an empty end_turn.
	if terr := finishClass.CheckTurn(finishReason, textChars > 0, hasToolUse || cr.droppedCalls > 0); terr != nil && !prefillOnly {
		logx.LogGateway("[#%d] << non-streaming turn failed (retryable): finishReason=%q err=%v usage in=%d out=%d", p.RID, finishReason, terr, inTok, outTok)
		logFailure(terr.Status, terr.Error())
		return false, terr.Status, terr
	}

	// Gemini is not given the client's stop_sequences (it would hide that
	// one fired); the gateway cuts the text itself, same as the stream FSM.
	applyStopSequences(claudeResp, usageMap, req.StopSequences, thinkingTok)

	enforceMaxTokens(claudeResp, usageMap, req.MaxTokens, outTok-thinkingTok, thinkingTok, hasToolUse)

	if p.HasSafeguards {
		toolUsesMap := make(map[string]interface{})
		for _, tid := range toolCallIDs {
			toolUsesMap[tid] = map[string]interface{}{
				"type":    "evaluated",
				"outcome": "not_flagged",
			}
		}
		claudeResp["safeguard_results"] = []interface{}{
			map[string]interface{}{
				"type": "dangerous_tool_use",
				"status": map[string]interface{}{
					"type":      "available",
					"tool_uses": toolUsesMap,
				},
			},
		}
	}

	readCount := 0
	madeProgress := false
	for _, sig := range toolCallSignatures {
		name, isReadEquiv, isWriteEquiv := loopguard.ReadLoopEquivalentToolName(sig)
		if isReadEquiv {
			readCount++
		} else if toolkind.IsModifyingToolName(name) || isWriteEquiv {
			madeProgress = true
		}
	}
	realProgress := madeProgress
	if readCount == 0 && !madeProgress {
		madeProgress = textChars > 200 || thinkingChars > 2000
	}
	loopKey := p.AgentKey + "|" + p.SessionID + "|" + p.InstanceID
	isPlanModeNow := loopguard.IsPlanModeRequest(claudeBodyBytes)
	readWindowSize, _ := loopguard.ReadLoopLimits(p.AgentKey, loopguard.HasModifyingToolsInRequest(claudeBodyBytes))
	loopguard.Global().RecordReadTurnWithRealProgress(loopKey, readCount, madeProgress, realProgress, readWindowSize, isPlanModeNow)
	hasIgnoreTag := loopguard.HasIgnoreLoopTag(lastText) || loopguard.HasIgnoreLoopTag(lastThinking) || loopguard.HasIgnoreLoopTag(string(claudeBodyBytes))
	// See streaming.go's identical filter for why: a status-check poll
	// repeated every turn while legitimately waiting on async work must
	// not feed the oscillation detector.
	oscillationCalls := make([]string, 0, len(toolCallSignatures))
	for _, sig := range toolCallSignatures {
		if !loopguard.IsStatusCheckToolCall(sig) {
			oscillationCalls = append(oscillationCalls, sig)
		}
	}
	loopguard.Global().RecordToolCalls(loopKey, oscillationCalls, hasIgnoreTag)

	// No placeholder text is injected for an empty content array: a refusal
	// or max_tokens-before-any-output legitimately returns content:[] in
	// Anthropic's real API, and the old "(empty response)" / "I'm not able
	// to help" fillers were stored by clients as real assistant turns.
	// GAP-D1-06: a turn that produced nothing beyond the client's own
	// prefill still returns the prefix (real client text, not a filler).
	if contentBlocks, ok := claudeResp["content"].([]map[string]interface{}); ok && len(contentBlocks) == 0 &&
		p.PrefillText != "" && claudeResp["stop_reason"] != "refusal" {
		claudeResp["content"] = append(contentBlocks, map[string]interface{}{
			"type": "text",
			"text": p.PrefillText,
		})
	}

	if claudeResp["stop_reason"] != "refusal" {
		applyPrefillToResponse(claudeResp, p.PrefillText)
	}
	stripOscIgnoreTag(claudeResp)
	applyPrewarmShape(claudeResp, p.RawClaudeBody)

	outJSON, _ := json.Marshal(claudeResp)
	ratelimit.ApplyHeaders(w.Header(), st.ActiveAccount, st.ResolvedModelName)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(outJSON)
	speccapture.Global().RecordClaudeResponse(p.RID, w.Header(), outJSON)

	logx.LogGateway("[#%d] << finishReason=%q stop_reason=%v usage in=%d out=%d cache_read=%d cache_creation=%d tools=%v",
		p.RID, finishReason, claudeResp["stop_reason"], uncachedInTok, outTok, cacheTok, cacheCreationTok, toolsCalled)

	durMs := time.Since(st.StartedAt).Milliseconds()
	var hitRate float64
	if inTok > 0 {
		hitRate = (float64(cacheTok) / float64(inTok)) * 100.0
	}

	requestlog.Add(requestlog.RequestLog{
		Timestamp:       st.StartedAt,
		Key:             p.ClientKey,
		KeyMasked:       requestlog.MaskKey(p.ClientKey),
		Account:         accLabel,
		SessionID:       p.SessionID,
		AgentKey:        p.AgentKey,
		AgentType:       p.AgentType,
		SubagentName:    p.SubagentName,
		InstanceID:      p.InstanceID,
		Model:           st.ResolvedModelName,
		TTFT:            durMs,
		TotalDur:        durMs,
		InTokens:        inTok,
		OutTokens:       outTok,
		CacheReadTokens: cacheTok,
		CacheHitRate:    hitRate,
		Tools:           toolsCalled,
		WasPanicked:     false,
		Shrunk:          p.WasShrunk,
		Status:          http.StatusOK,
	})

	return true, http.StatusOK, nil
}
