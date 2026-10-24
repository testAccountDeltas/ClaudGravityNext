// Part of the httpapi package — see errors.go for the package doc
// comment. POST /v1/messages — the core of this package. A faithful port
// of v1's handleMessages, but thin: everything from account selection
// onward (the retry/failover loop) is internal/orchestrate.Execute's job
// now, not this handler's. See pipeline.go's own doc comment in
// internal/orchestrate for the exact scope boundary this file respects.
package httpapi

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/accounts"
	"claudgravity/internal/anthropicerr"
	"claudgravity/internal/config"
	"claudgravity/internal/ctxedit"
	"claudgravity/internal/editrepair"
	"claudgravity/internal/logx"
	"claudgravity/internal/orchestrate"
	"claudgravity/internal/reqctx"
	"claudgravity/internal/requestlog"
	"claudgravity/internal/speccapture"
	"claudgravity/internal/translate"
)

var reqIDCounter uint64

// nextReqID mints this process's own monotonically-increasing request
// counter (distinct from nextRequestID in middleware.go, which mints the
// client-facing req_<hex> header value) — used for internal log
// correlation only, exactly as in v1.
func nextReqID() uint64 {
	return atomic.AddUint64(&reqIDCounter, 1)
}

func handleMessages(cfgMgr *config.ConfigManager, w http.ResponseWriter, r *http.Request) {
	rid := nextReqID()
	t0 := time.Now()

	// 1. Resolve tenant by API key. Claude Code's own OAuth/subscription
	// flow sends Authorization: Bearer — any official Anthropic SDK or a
	// client configured with a plain ANTHROPIC_API_KEY sends x-api-key by
	// default.
	clientKey := r.Header.Get("x-api-key")
	if clientKey == "" {
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			clientKey = strings.TrimSpace(authHeader[7:])
		} else {
			clientKey = authHeader
		}
	}
	if strings.TrimSpace(clientKey) == "" {
		logx.LogGateway("[#%d] Unauthorized: invalid or missing Bearer token", rid)
		WriteAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid or missing API key")
		return
	}
	tenant, ok := cfgMgr.GetTenant(clientKey)
	if !ok {
		logx.LogGateway("[#%d] Unauthorized: invalid or missing Bearer token", rid)
		WriteAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid or missing API key")
		requestlog.Add(requestlog.RequestLog{
			Timestamp: t0,
			Key:       redactedKey(clientKey),
			KeyMasked: redactedKey(clientKey),
			Account:   "-",
			SessionID: "-",
			AgentKey:  "main",
			AgentType: "main",
			Model:     "-",
			Status:    http.StatusUnauthorized,
			Error:     "Missing API key",
		})
		return
	}

	agentKey, agentType, subagentName, instanceID := requestlog.ParseAgentInfo(r)
	reqClass := r.Header.Get("x-claude-code-request-class")

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			WriteAnthropicError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body exceeds the 25MB limit")
			return
		}
		WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Failed to read request body")
		return
	}

	if betaHeaders := r.Header.Values("anthropic-beta"); len(betaHeaders) > 0 {
		bodyBytes = mergeAnthropicBetaHeader(bodyBytes, strings.Join(betaHeaders, ","))
	}

	speccapture.Global().Begin(rid)
	speccapture.Global().RecordClaudeRequest(rid, r.Header, bodyBytes)
	defer speccapture.Global().Finish(rid)

	if errMsg, valid := validateMessagesRequest(bodyBytes); !valid {
		WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", errMsg)
		return
	}

	// Hint-header fallback: classify helper requests (title/summary/topic)
	// that arrive without x-claude-code-request-class, and treat a
	// parent-agent-id header as a subagent request. Unknown/unsupported
	// request fields are accepted and ignored (see logIgnoredFields).
	reqClass = classifyReqClass(r, reqClass, bodyBytes)
	logIgnoredFields(rid, bodyBytes)

	// 2. Derive Conversation Anchor for Session Affinity (Prompt Caching).
	headerSessionID := r.Header.Get("x-claude-code-session-id")
	if headerSessionID == "" {
		headerSessionID = r.Header.Get("x-session-id")
	}
	if headerSessionID == "" {
		// GAP-T11: the request's own metadata.session_id (Anthropic's
		// documented session-identification field for SDK callers that
		// aren't Claude Code CLI, which only sends the header above) was
		// parsed into claude.Request but never consulted anywhere —
		// falling back to it here lets a non-CLI SDK client still get
		// sticky-account/session-affinity routing instead of silently
		// landing on the content-hash last resort every single call.
		headerSessionID = gjson.GetBytes(bodyBytes, "metadata.session_id").String()
	}
	tenantPrefix := requestlog.MaskKey(clientKey)
	convAnchor := tenantPrefix + ":" + accounts.DeriveConversationAnchor(bodyBytes, headerSessionID, instanceID)

	// 2.1 Fast-Path Intercept: Claude Code Native WebSearch Sub-Request.
	if tryDirectWebSearchIntercept(r.Context(), rid, t0, clientKey, tenant, bodyBytes, w) {
		return
	}

	// 2.2 Fast-Path Intercept: Claude Code 1-token rate-limit probe.
	if tryQuotaProbeIntercept(clientKey, tenant, bodyBytes, w) {
		return
	}

	// Derived before translation (geminiBody doesn't exist yet) so
	// DeriveSessionID's own last-resort content-hash fallback has a
	// stable stand-in to hash against. Namespaced by tenantPrefix so
	// session affinity and caches are strictly isolated per tenant.
	sessionID := tenantPrefix + ":" + requestlog.DeriveSessionID(r, bodyBytes, bodyBytes)

	// Native 3P model names pass through tiering unchanged — remap to
	// the Gemini-mapped equivalent if the sticky account for this
	// conversation has already exhausted its 3P quota.
	bodyBytes = remapExhaustedNative3PModel(rid, convAnchor, bodyBytes)

	// GAP-ED-03: this CWD/file-snapshot registration used to run AFTER
	// translation below — on a session's very FIRST turn, that meant
	// translation's own dedup/relative-path resolution ran against a
	// CWD that hadn't been recorded yet at all. Auto-detect and record
	// session working directory + file snapshots for Edit repair BEFORE
	// translation, so this same request can already use it. Keyed per
	// subagent instance, not just the terminal-wide sessionID: a
	// subagent spawned in its own git worktree legitimately has a
	// different cwd than the main thread or a sibling subagent.
	cwdKey := sessionID
	if instanceID != "" {
		cwdKey = sessionID + ":" + instanceID
	}
	if cwd := editrepair.ExtractCWDFromSystemPrompt(editrepair.ExtractSystemPromptText(bodyBytes)); cwd != "" {
		editrepair.RecordSessionCWD(cwdKey, cwd)
	}
	editrepair.RecordSessionFileSnapshots(cwdKey, bodyBytes)

	// Context editing (context_management.edits): the gateway clears old
	// tool results / thinking itself, Gemini has no such feature.
	ctxCfg, ctxErr := ctxedit.Parse(bodyBytes)
	if ctxErr != nil {
		WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", ctxErr.Error())
		return
	}
	var contextManagement map[string]interface{}
	if ctxCfg.Present {
		ctxRes, err := ctxedit.Apply(bodyBytes, ctxCfg)
		if err != nil {
			logx.LogGateway("[#%d] context editing failed, sending history unedited: %v", rid, err)
		} else {
			bodyBytes = ctxRes.Body
			contextManagement = ctxedit.Response(ctxRes.Applied)
			for _, e := range ctxRes.Applied {
				logx.LogGateway("[#%d] context editing: %v (prompt ~%d -> ~%d tokens)", rid, e, ctxRes.OriginalTokens, ctxRes.Tokens)
			}
		}
		bodyBytes = ctxedit.StripField(bodyBytes)
	}

	// 3. Full translation: tiering, LoopGuard, middle-truncation,
	// sanitizer.
	serverCfg := cfgMgr.GetServerConfig()
	translate.SetAllowHistoryTruncation(serverCfg.AllowHistoryTruncation)
	isAntigravity := !(tenant.GoogleAuthType == "api_key" && tenant.GoogleAPIKey != "" && tenant.GoogleAPIKey != "YOUR_GEMINI_API_KEY_HERE")
	result, err := translate.ConvertClaudeRequestToGemini(bodyBytes, tenant, reqClass, agentKey, sessionID, instanceID, isAntigravity)
	if err != nil {
		var overflow *translate.ContextOverflowError
		if errors.As(err, &overflow) {
			logx.LogGateway("[#%d] Context overflow (pre-flight): %v", rid, err)
			WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", overflow.Error())
			return
		}
		logx.LogGateway("[#%d] Translation error: %v", rid, err)
		WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Translation error: "+err.Error())
		return
	}
	// Check inline data ceiling on post-truncation Gemini payload so that
	// sessions with older image attachments pruned by TruncateConversationToFit
	// are not permanently deadlocked with 413.
	if totalInlineBytes := estimateGeminiInlineDataBytes(gjson.ParseBytes(result.GeminiBody)); totalInlineBytes > maxInlineDataBytes {
		WriteAnthropicError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Combined size of inline image/document data exceeds the 20MB upstream limit")
		return
	}
	speccapture.Global().RecordGeminiRequest(rid, nil, result.GeminiBody)
	speccapture.Global().SetSessionMeta(rid, sessionID, agentKey)

	isStream := gjson.GetBytes(bodyBytes, "stream").Bool()

	hasSafeguards := gjson.GetBytes(bodyBytes, "safeguards").Exists()
	_, hasFlusher := w.(http.Flusher)

	p := &reqctx.Params{
		RID:                  rid,
		SessionID:            sessionID,
		ClientKey:            clientKey,
		Tenant:               tenant,
		ServerCfg:            serverCfg,
		ModelName:            gjson.GetBytes(bodyBytes, "model").String(),
		AgentKey:             agentKey,
		AgentType:            agentType,
		SubagentName:         subagentName,
		InstanceID:           instanceID,
		ConvAnchor:           convAnchor,
		IsStreaming:          isStream && hasFlusher,
		IsAntigravity:        isAntigravity,
		WasShrunk:            result.WasShrunk,
		HasSafeguards:        hasSafeguards,
		PrefillText:          result.PrefillText,
		SyntheticReasonTools: result.SyntheticReasonTools,
		ReverseToolNameMap:   result.ReverseToolNameMap,
		RawClaudeBody:        bodyBytes,
		ContextManagement:    contextManagement,
	}
	st := &reqctx.State{
		StartedAt:         t0,
		ResolvedModelName: result.ResolvedModel,
		GeminiBody:        result.GeminiBody,
		EstInputTokens:    result.EstimatedInputTokens,
	}

	if isStream {
		setSSEHeaders(w, r)
	}
	o := &orchestrate.Orchestrator{}
	wrote, status, execErr := o.Execute(r.Context(), p, st, w)
	if wrote {
		return
	}
	if execErr != nil {
		if status != 499 { // 499 = client went away; Execute already logged that
			logx.LogGateway("[#%d] !! All pre-stream failover attempts failed: %v", rid, execErr)
		}
		// Google's context-overflow 400 -> Anthropic's exact wording, which is
		// what Claude Code's auto-compaction keys on (streaming and not).
		if anthropicerr.IsContextExceeded(status, execErr.Error()) {
			WriteAnthropicError(w, http.StatusBadRequest, "invalid_request_error",
				anthropicerr.PromptTooLongMessage(execErr.Error(), int64(st.EstInputTokens), translate.ContextWindowCeiling(result.ResolvedModel)))
			return
		}
		// The raw upstream text (possibly Google JSON) stays in the log
		// line above; the client gets a short sanitised message.
		WriteAnthropicError(w, status, anthropicerr.TypeForStatus(status), ClientUpstreamMessage(status, execErr.Error()))
	}
}

// mergeAnthropicBetaHeader merges the incoming HTTP anthropic-beta header
// into the JSON request body's "betas" array so both body and header flags
// reach the translation layer.
func mergeAnthropicBetaHeader(bodyBytes []byte, headerVal string) []byte {
	var newBetas []string
	for _, b := range strings.Split(headerVal, ",") {
		b = strings.TrimSpace(b)
		if b != "" {
			newBetas = append(newBetas, b)
		}
	}
	if len(newBetas) == 0 {
		return bodyBytes
	}
	existing := gjson.GetBytes(bodyBytes, "betas")
	seen := make(map[string]bool)
	var merged []string
	if existing.IsArray() {
		for _, item := range existing.Array() {
			val := item.String()
			if val != "" && !seen[val] {
				seen[val] = true
				merged = append(merged, val)
			}
		}
	}
	for _, b := range newBetas {
		if !seen[b] {
			seen[b] = true
			merged = append(merged, b)
		}
	}
	updated, err := sjson.SetBytes(bodyBytes, "betas", merged)
	if err == nil {
		return updated
	}
	return bodyBytes
}

// redactedKey is what the request log stores for a credential that failed
// authentication: never the submitted value itself (it may be a typo of a
// real key), only a short prefix and its length.
func redactedKey(k string) string {
	k = strings.TrimSpace(k)
	if len(k) <= 4 {
		return "<redacted>"
	}
	return k[:4] + "...(" + strconv.Itoa(len(k)) + ")"
}
