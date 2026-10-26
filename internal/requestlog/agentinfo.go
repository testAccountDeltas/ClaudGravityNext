// Part of the requestlog package's port from superrelay (v1)'s
// request_logger.go — see store.go for the split rationale.
package requestlog

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"claudgravity/internal/sessionkey"
)

// ParseAgentInfo also returns instanceID, used to tell apart N concurrent
// same-named subagents (e.g. 3 simultaneous Explore calls, which would
// otherwise all collapse into one indistinguishable "sub:Explore" branch).
//
// An earlier version of this comment claimed x-claude-code-prompt-id was
// the stable per-instance key and x-claude-code-agent-id was not. That was
// backwards, confirmed by two independent live captures (this gateway's
// own local traffic, and genuine claude-cli traffic against real
// api.anthropic.com via mitmdump): when the orchestrator spawns two
// parallel "general-purpose" subagents in one "Agent" tool_use batch, BOTH
// share the exact same Prompt-Id for their entire run, while each gets its
// own Agent-Id that stays constant across all of its own turns. Prompt-Id
// is effectively the *batch* id (shared by every subagent launched
// together); Agent-Id is the *instance* id. Only subagents carry
// Agent-Id at all (the main/orchestrator thread never does, per
// claude-cli's own Kp(agentType==="main") check), which is also why it's
// never used as a fallback for "main"/"auxiliary" below.
func ParseAgentInfo(r *http.Request) (agentKey, agentType, subagentName, instanceID string) {
	reqClass := r.Header.Get("x-claude-code-request-class")
	rawAgentType := r.Header.Get("x-claude-code-agent-type")
	promptID := r.Header.Get("x-claude-code-prompt-id")
	agentID := r.Header.Get("x-claude-code-agent-id")
	if agentID == "" {
		agentID = r.Header.Get("x-claude-subagent-id")
	}
	if agentID == "" {
		agentID = r.Header.Get("x-subagent-id")
	}
	if agentID == "" {
		agentID = r.Header.Get("anthropic-subagent-id")
	}

	if reqClass == "auxiliary" {
		return "auxiliary", "auxiliary", "", ""
	}
	if rawAgentType != "" {
		if agentID != "" {
			return "sub:" + rawAgentType, "subagent", rawAgentType, agentID
		}
		return "sub:" + rawAgentType, "subagent", rawAgentType, promptID
	}
	if reqClass == "subagent" {
		if agentID != "" {
			return "subagent", "subagent", "subagent", agentID
		}
		return "subagent", "subagent", "subagent", promptID
	}
	// Only subagents carry an Agent-Id (see above), so one without the
	// agent-type / request-class hint headers is still a subagent. Without
	// this, a client that does not send those hints had every subagent
	// request counted as the main thread: shared LoopGuard counters, one cwd
	// key and one account for all of them, and no cheap tier for Explore.
	if agentID != "" {
		return "subagent", "subagent", "subagent", agentID
	}
	// The main/orchestrator thread's own requests used to fall back to
	// Prompt-Id here too (same as a Task-tool-invocation subagent with no
	// Agent-Id of its own) — but Prompt-Id is stable only for the span of
	// one Task-tool invocation / user turn, not the whole conversation, so
	// a non-empty instanceID here changed every single turn. Every caller
	// that qualifies a cache key by instanceID when non-empty
	// (antigravitySessionKey/sessionBumpMap/cwdKey in server.go, loopKey
	// here and in translator.go) was therefore getting a brand new key for
	// the main thread on every turn, defeating Google's server-side prefix
	// cache continuously for the single most common case in this gateway.
	// Main never carries a real Agent-Id (per claude-cli's own
	// agentType==="main" check, confirmed above), so it has no legitimate
	// per-instance key to qualify by at all — must stay empty.
	return "main", "main", "", ""
}

// DeriveSessionID picks a stable session identifier for one request, in
// priority order: an explicit session header, metadata.session_id in the
// body, a hash of the first user turn's text, or finally a hash of the
// translated Gemini payload via sessionkey.GenerateStableSessionID (the
// same stable-id derivation internal/warmup uses for Antigravity session
// stickiness — see that package's doc comment for why this now lives in
// its own internal/sessionkey package instead of either of them).
func DeriveSessionID(r *http.Request, bodyBytes []byte, geminiBody []byte) string {
	if s := r.Header.Get("x-session-id"); s != "" {
		return s
	}
	if s := r.Header.Get("x-claude-code-session-id"); s != "" {
		return s
	}
	if s := r.Header.Get("anthropic-session-id"); s != "" {
		return s
	}
	if s := r.Header.Get("x-anthropic-session-id"); s != "" {
		return s
	}
	if s := gjson.GetBytes(bodyBytes, "metadata.session_id").String(); s != "" {
		return s
	}

	msgs := gjson.GetBytes(bodyBytes, "messages")
	if msgs.IsArray() {
		for _, m := range msgs.Array() {
			if m.Get("role").String() == "user" {
				txt := m.Get("content.0.text").String()
				if txt == "" {
					txt = m.Get("content").String()
				}
				if txt != "" {
					h := sha256.Sum256([]byte(txt))
					return "sess-" + hex.EncodeToString(h[:4])
				}
			}
		}
	}

	rawSess := sessionkey.GenerateStableSessionID(geminiBody, 0, "")
	rawClean := strings.TrimPrefix(rawSess, "-")
	if len(rawClean) > 8 {
		rawClean = rawClean[:8]
	}
	return "sess-" + rawClean
}
