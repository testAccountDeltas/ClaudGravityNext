// Package translate ports superrelay (v1)'s translator.go —
// ConvertClaudeRequestToGemini, the core of Claude-to-Gemini request
// translation (sanitization, tiering/thinking-budget resolution, message
// and tool-declaration translation, generation config, safety settings).
//
// Per ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md's inventory,
// translator.go is one of the 5 files explicitly flagged as the source
// of 90% of the regressions across 15 rounds of audits — rewritten
// around the typed protocol only at its two edges (sanitize/truncate via
// internal/sanitize and internal/tokens, both already typed
// claude.Request in/out), while the high-risk Claude-block-to-Gemini-part
// translation in the middle stays on gjson/sjson, transcribed near-
// literally rather than rewritten — the same "minimal diff where risk is
// high" judgment already applied to internal/schema and
// internal/stream/chunk.go in this same migration.
//
// This file is only orchestration: it calls the phase functions in
// toolnames.go/loopnudge.go/system.go/messages.go/tools.go/
// generation.go in v1's own fixed order, with no business logic of its
// own beyond that sequencing — matching the target tree's own stated
// intent for translate.go (ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md
// Part 4.3).
package translate

import (
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts"
	"claudgravity/internal/accounts/tiering"
	"claudgravity/internal/config"
	"claudgravity/internal/logx"
	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/sanitize"
	"claudgravity/internal/tokens"
)

// Result is ConvertClaudeRequestToGemini's single return value — one
// struct instead of v1's 8 positional return values, per the migration
// spec's Part 1.1 diagnosis of a long positional tuple as a recurring
// source of a caller silently reading the wrong slot (the two
// string-shaped PrefillText/ReverseToolNameMap positions are the exact
// case the spec calls out as indistinguishable at the call site without
// reading this function's signature).
type Result struct {
	GeminiBody    []byte
	ResolvedModel string
	// ThinkingBudget may differ from what internal/accounts/tiering first
	// picked: applyGenerationConfig can lower it to fit under a model's
	// output-token ceiling.
	ThinkingBudget int
	WasShrunk      bool
	// SyntheticReasonTools names every tool whose Gemini-bound schema only
	// has a "reason" property because its real schema had none at all —
	// the caller (internal/stream, once wired) must strip that key back
	// out of the model's actual function-call args before handing them to
	// Claude Code.
	SyntheticReasonTools map[string]bool
	// ReverseToolNameMap maps a Gemini-sanitized function name back to the
	// original Claude-side tool name, for the response path to restore
	// once Gemini invokes the function by its sanitized one.
	ReverseToolNameMap   map[string]string
	PrefillText          string
	EstimatedInputTokens int
}

// fallbackMaxTokens covers the known Gemini model family ceilings for the
// rare case the live model catalog is empty (startup FetchAvailableModels
// failed, or ran before the network was up) — without this, truncation
// would silently disable itself entirely rather than erring conservative,
// and a request would sail right past the 1M-token wall into a raw
// Google 400 instead of being proactively trimmed.
var fallbackMaxTokens = map[string]int64{
	"gemini-3.8-flash":        1048576,
	"gemini-3.8-flash-tiered": 1048576,
	"gemini-3.8-flash-low":    1048576,
	"gemini-3.8-flash-medium": 1048576,
	"gemini-3.8-flash-high":   1048576,
	"gemini-4-argon":          2097152,
	"gemini-2.5-flash":        1048576,
	"gemini-2.5-pro":          2097152,
	"gemini-2.5-flash-lite":   1048576,
}

// allowHistoryTruncation is the opt-in for silently dropping the oldest
// conversation turns when the request does not fit the context window.
// Off by default: an overflowing request is rejected with Anthropic's
// "prompt is too long" error (see ContextOverflowError) so the client can
// compact properly instead of the model answering from a gutted history.
var allowHistoryTruncation atomic.Bool

// SetAllowHistoryTruncation enables or disables silent history truncation
// (config: allow_history_truncation).
func SetAllowHistoryTruncation(v bool) { allowHistoryTruncation.Store(v) }

// ContextOverflowError is returned when the request cannot fit the model's
// context window; the HTTP layer maps it to a 400 invalid_request_error
// "prompt is too long: N tokens > M maximum".
type ContextOverflowError struct {
	Estimated int64
	Limit     int64
}

func (e *ContextOverflowError) Error() string {
	return fmt.Sprintf("prompt is too long: %d tokens > %d maximum", e.Estimated, e.Limit)
}

// ContextWindowCeiling exposes the model's context-window size to the
// HTTP layer (for the upstream-overflow error mapping).
func ContextWindowCeiling(modelName string) int64 { return resolveContextWindowCeiling(modelName) }

// resolveContextWindowCeiling resolves modelName's real context-window
// token ceiling: internal/tokens.TruncateConversationToFit deliberately
// inverted this dependency away from internal/accounts (see that
// function's own doc comment) — this package already depends on
// internal/accounts for the model catalog (applyGenerationConfig's own
// MaxOutput/Thinking lookups), so it's the natural owner of this
// resolution step until internal/orchestrate exists to take it over.
func resolveContextWindowCeiling(modelName string) int64 {
	modelMeta, hasMeta := accounts.ModelCatalog().GetModel(modelName)
	if hasMeta && modelMeta.MaxTokens > 0 {
		return modelMeta.MaxTokens
	}
	if ceiling := fallbackMaxTokens[modelName]; ceiling > 0 {
		return ceiling
	}
	// A model name present in neither the (possibly empty) live catalog
	// nor this hardcoded fallback map — a new Gemini release, or a
	// custom/renamed model — must not silently disable truncation
	// entirely. Trimming a request that didn't need it is harmless;
	// sailing an oversized one straight into Google's raw context-length
	// 400 is not.
	return 1048576
}

// tieringInputMap builds the small map[string]interface{} view of req
// internal/accounts/tiering.ResolveTieringAndEffort needs (model,
// output_config, thinking) — that function's signature predates the
// typed claude.Request domain and is shared with internal/accounts'
// other callers, so this package adapts to it rather than changing it.
func tieringInputMap(req claude.Request) map[string]interface{} {
	m := map[string]interface{}{"model": req.Model}
	if len(req.OutputConfig) > 0 {
		var oc map[string]interface{}
		if json.Unmarshal(req.OutputConfig, &oc) == nil {
			m["output_config"] = oc
		}
	}
	if len(req.Thinking) > 0 {
		var th map[string]interface{}
		if json.Unmarshal(req.Thinking, &th) == nil {
			m["thinking"] = th
		}
	}
	if len(req.Messages) > 0 {
		var msgs []interface{}
		for _, msg := range req.Messages {
			msgMap := map[string]interface{}{"role": string(msg.Role)}
			if len(msg.OutputConfig) > 0 {
				var oc map[string]interface{}
				if json.Unmarshal(msg.OutputConfig, &oc) == nil {
					msgMap["output_config"] = oc
				}
			}
			msgs = append(msgs, msgMap)
		}
		m["messages"] = msgs
	}
	return m
}

// ConvertClaudeRequestToGemini converts an Anthropic Messages API payload
// into a Google Gemini streamGenerateContent payload with full
// sanitization and tiering. agentKey/sessionID/instanceID key the
// LoopGuard nudge state (internal/loopguard) the same way v1's loopKey
// did.
func ConvertClaudeRequestToGemini(claudeJSON []byte, tenant config.TenantConfig, reqClass, agentKey, sessionID, instanceID string, isAntigravity bool) (Result, error) {
	var req claude.Request
	if err := json.Unmarshal(claudeJSON, &req); err != nil {
		return Result{}, fmt.Errorf("invalid json: %w", err)
	}

	// 1. Dynamic Tiering & Model Effort Resolution.
	modelName, budget := tiering.ResolveTieringAndEffort(tieringInputMap(req), reqClass, agentKey)
	if modelName == "" {
		modelName = tenant.DefaultModel
	}

	// 1.5 Context-window-aware truncation: drop oldest history if it
	// would exceed the REAL context window of the Gemini sub-model
	// tiering just picked above. requestedMaxOutputHint must mirror the
	// SAME inflation applyGenerationConfig applies to requestedMax
	// (thinking budget forces max_tokens up to budget+4096) — otherwise
	// this reserves too little room for the response and under-truncates.
	var requestedMaxOutputHint int64 = 8192
	if req.MaxTokens > 0 {
		requestedMaxOutputHint = req.MaxTokens
	}
	if budget > 0 && requestedMaxOutputHint <= int64(budget) {
		requestedMaxOutputHint = int64(budget) + 4096
	} else if budget < 0 {
		// Must mirror applyGenerationConfig's own ModeAuto inflation
		// below (maxOutputTokens raised to the model's full ceiling,
		// since thinking and the visible answer share that one pool
		// under ModeAuto) — otherwise this reserves only ~8192 tokens of
		// headroom while the real request reserves up to 64000, and a
		// conversation sitting near the model's context ceiling gets
		// under-truncated here only to blow past Google's real token
		// wall once the inflated output budget is added.
		if earlyMeta, ok := accounts.ModelCatalog().GetModel(modelName); ok && earlyMeta.MaxOutput > 0 {
			requestedMaxOutputHint = earlyMeta.MaxOutput
		} else {
			requestedMaxOutputHint = 64000
		}
	}

	// 1. Initial AST Sanitization (middle truncation, role repair, system
	// extraction).
	sanResult := sanitize.SanitizeMessagesAndGraph(req)
	req = sanResult.Request
	wasShrunk := sanResult.TruncatedAny
	prefillText := sanResult.PrefillText

	// Context Window Safety Truncation: runs AFTER initial sanitization
	// so token budget is measured against middle-truncated outputs,
	// preventing premature or unnecessary message dropping.
	ceiling := resolveContextWindowCeiling(modelName)
	prof := tokens.ProfileForModel(modelName)
	dedupSessionKey := sessionID
	if instanceID != "" {
		dedupSessionKey = sessionID + ":" + instanceID
	}
	// Byte-based attachment pruning (oldest first, including images nested
	// in tool_result) keeps the total under the 20MB inlineData ceiling; the
	// pruned copy replaces req whether or not any message gets dropped.
	if pruned, n := tokens.PruneAttachmentsByBytes(req, tokens.MaxInlineAttachmentBytes); n > 0 {
		req = pruned
		wasShrunk = true
		logx.LogGateway("[Attachments] pruned %d oldest image/document attachment(s) to stay under %d bytes [%s]", n, tokens.MaxInlineAttachmentBytes, agentKey)
	}
	// History truncation is opt-in; by default an overflowing prompt is
	// rejected by the fit check below instead of being silently gutted.
	truncatedReq, dropped, truncated := req, 0, false
	if allowHistoryTruncation.Load() {
		truncatedReq, dropped, truncated = tokens.TruncateConversationToFit(req, ceiling, requestedMaxOutputHint, dedupSessionKey)
	}
	if truncated {
		req = truncatedReq
		wasShrunk = true
		logx.LogGateway("[ContextWindow] dropped %d oldest message(s) to fit %s's context window [%s]", dropped, modelName, agentKey)
		// Re-sanitize to repair role alternation and orphaned tool calls
		// created by dropping intermediate turns.
		resanResult := sanitize.SanitizeMessagesAndGraph(req)
		req = resanResult.Request
		if resanResult.PrefillText != "" {
			prefillText = resanResult.PrefillText
		}
	}

	// Dedup repeated Read results AFTER truncation (GAP-SN2): must run as the
	// last step before serializing, after every truncation pass has already decided
	// which messages survive, so pointers never refer to dropped turns.
	var dedupChanged bool
	req, dedupChanged = sanitize.DedupRepeatedReads(req, dedupSessionKey)
	if dedupChanged {
		wasShrunk = true
	}

	// 2. Loop-Guard Nudges (Read-Loop & Oscillation) — skipped when there is an active
	// assistant prefill continuation at the tail of the conversation, preventing loop-guard
	// warning text from contaminating the prefill continuation instruction (GAP-V2-08).
	if prefillText == "" {
		req = applyLoopGuardNudges(req, agentKey, sessionID, instanceID)
	}

	// 3. Final graph validation after any loop-guard nudges.
	finalSanResult := sanitize.SanitizeMessagesAndGraph(req)
	req = finalSanResult.Request
	if finalSanResult.TruncatedAny {
		wasShrunk = true
		if finalSanResult.PrefillText != "" {
			prefillText = finalSanResult.PrefillText
		}
	}

	// Re-serialize sanitized request.
	sanitizedBytes, err := json.Marshal(req)
	if err != nil {
		return Result{}, fmt.Errorf("re-marshal sanitized json: %w", err)
	}
	// Tools the client executed earlier but no longer lists stay callable
	// (see historytools.go) instead of being replayed as text notes.
	sanitizedBytes = declareToolsUsedInHistory(sanitizedBytes)
	root := gjson.ParseBytes(sanitizedBytes)
	out := "{}"

	// 4. System Instruction (scrubbing billing headers and injecting loop notices).
	loopKey := agentKey + "|" + sessionID + "|" + instanceID
	var pendingNudgeKeys []string
	if prefillText == "" {
		if sessionID != "" {
			pendingNudgeKeys = append(pendingNudgeKeys, sessionID)
		}
		if loopKey != "" && loopKey != sessionID {
			pendingNudgeKeys = append(pendingNudgeKeys, loopKey)
		}
	}
	out = applySystemInstruction(out, root, pendingNudgeKeys...)

	// 5. Messages Translation.
	// Grep/Glob are stripped only for the pre-3.x models; Gemini 3.x gets
	// them unless the tenant opts back in (strip_search_tools_gemini3).
	tenant.StripSearchTools = tenant.StripSearchTools && (!isGemini3(modelName) || tenant.StripSearchToolsGemini3)
	tn := computeToolNameInfo(root, tenant.StripSearchTools)
	if err := validateToolChoice(root, tn); err != nil {
		return Result{}, err
	}
	translateSessionKey := sessionID
	if instanceID != "" {
		translateSessionKey = sessionID + ":" + instanceID
	}
	out = translateMessages(out, root, tn, isAntigravity, translateSessionKey)

	// 6. Tools Declaration.
	out, syntheticReasonTools, reverseToolNameMap := applyToolsDeclaration(out, root, tenant, tn, sessionID)
	if isAgentSummaryFork(root) {
		out = forbidToolCalls(out)
		logx.LogGateway("[Tools] background-agent label request: tool calls disabled [%s]", agentKey)
	}

	// 7-8. Thinking & Output Tokens Configuration, sampling, structured
	// outputs, safety settings.
	out, budget = applyGenerationConfig(out, root, modelName, budget, isAntigravity)

	estInputTokens := tokens.WeightedRequestTokensFor(req, prof)
	// Reject (instead of silently truncating) a prompt that cannot fit. The
	// weighted estimate is used on purpose: the flat conservative one
	// (2 bytes/token) would reject prompts Gemini accepts.
	if limit := tokens.ContextBudget(ceiling, requestedMaxOutputHint); int64(estInputTokens) > limit {
		return Result{}, &ContextOverflowError{Estimated: int64(estInputTokens), Limit: limit}
	}

	return Result{
		GeminiBody:           []byte(out),
		ResolvedModel:        modelName,
		ThinkingBudget:       budget,
		WasShrunk:            wasShrunk,
		SyntheticReasonTools: syntheticReasonTools,
		ReverseToolNameMap:   reverseToolNameMap,
		PrefillText:          prefillText,
		EstimatedInputTokens: estInputTokens,
	}, nil
}
