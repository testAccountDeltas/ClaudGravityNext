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
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts"
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
	if st.BearerToken != "" {
		upReq.Header.Set("Authorization", "Bearer "+st.BearerToken)
	}
	if p.IsAntigravity {
		upReq.Header.Set("User-Agent", accounts.AntigravityUserAgent)
		upReq.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
		upReq.Header.Set("Client-Metadata", `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`)
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
	var cacheCreationTok int64
	if uncachedInTok > cacheCreationThreshold(req.Model) && hasCacheControlBreakpoint(claudeBodyBytes) {
		frac := cacheBreakpointPrefixFraction(claudeBodyBytes)
		cacheCreationTok = int64(float64(uncachedInTok) * frac)
		uncachedInTok -= cacheCreationTok
	}

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
		"cache_read_input_tokens":     cacheTok,
	}
	// GAP-TH-07
	if thinkingTok > 0 {
		usageMap["output_tokens_details"] = map[string]interface{}{
			"thinking_tokens": thinkingTok,
		}
	}

	stopReason := "end_turn"
	var stopDetails map[string]interface{}
	finishReason := geminiJSON.Get("candidates.0.finishReason").String()
	switch {
	case finishReason == "MAX_TOKENS":
		stopReason = "max_tokens"
	case finishReason == "SAFETY" || finishReason == "RECITATION" || finishReason == "BLOCKLIST" ||
		finishReason == "PROHIBITED_CONTENT" || finishReason == "SPII" || finishReason == "MALICIOUS" ||
		finishReason == "LANGUAGE" || finishReason == "IMAGE_SAFETY":
		stopReason = "refusal"
		stopDetails = map[string]interface{}{
			"type":        "refusal",
			"category":    "general_harms",
			"explanation": "Content blocked by safety policy",
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

	cand := geminiJSON.Get("candidates.0")
	cr := processCandidate(p, cand, claudeResp, claudeBodyBytes, req, cwdKey)
	toolsCalled, toolCallIDs, toolCallSignatures := cr.toolsCalled, cr.toolCallIDs, cr.toolCallSignatures
	textChars, thinkingChars, lastText, lastThinking := cr.textChars, cr.thinkingChars, cr.lastText, cr.lastThinking

	if claudeResp["stop_reason"] == "end_turn" {
		for _, seq := range req.StopSequences {
			if seq != "" && strings.HasSuffix(lastText, seq) {
				claudeResp["stop_reason"] = "stop_sequence"
				claudeResp["stop_sequence"] = seq
				// GAP-D1-07: Anthropic's contract excludes the matched
				// stop sequence from the returned text — the whole point
				// of asking for one is that the client never sees it.
				// The full response is already built here (unlike the
				// streaming path, which has already sent these exact
				// characters to the client over SSE before this check
				// ever runs and can't retroactively un-send them — that
				// side of this gap is left as a known limitation), so
				// trim it off the last text content block before
				// responding.
				if blocks, ok := claudeResp["content"].([]map[string]interface{}); ok {
					for i := len(blocks) - 1; i >= 0; i-- {
						if blocks[i]["type"] == "text" {
							if txt, ok := blocks[i]["text"].(string); ok {
								blocks[i]["text"] = strings.TrimSuffix(txt, seq)
							}
							break
						}
					}
				}
				break
			}
		}
	}

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

	if contentBlocks, ok := claudeResp["content"].([]map[string]interface{}); ok && len(contentBlocks) == 0 {
		// Anthropic's Message.content has minItems:1.
		// GAP-D1-06: honor prefill contract when model returns nothing.
		emptyText := "(empty response)"
		if claudeResp["stop_reason"] == "refusal" {
			emptyText = "I'm not able to help with that request."
		} else if p.PrefillText != "" {
			emptyText = p.PrefillText
		}
		claudeResp["content"] = append(contentBlocks, map[string]interface{}{
			"type": "text",
			"text": emptyText,
		})
	}

	if claudeResp["stop_reason"] != "refusal" {
		applyPrefillToResponse(claudeResp, p.PrefillText)
	}
	stripOscIgnoreTag(claudeResp)

	outJSON, _ := json.Marshal(claudeResp)
	ratelimit.ApplyHeaders(w.Header(), st.ActiveAccount, st.ResolvedModelName)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(outJSON)
	speccapture.Global().RecordClaudeResponse(p.RID, w.Header(), outJSON)

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
