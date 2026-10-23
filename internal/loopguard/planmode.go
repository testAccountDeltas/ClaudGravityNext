// Part of the loopguard package's port from superrelay (v1)'s
// loopguard.go — see bashdetect.go for the split rationale.
package loopguard

import (
	"strings"

	"github.com/tidwall/gjson"

	"claudgravity/internal/toolkind"
)

// exploreReadWindowSize/exploreReadWindowThreshold are the read-loop
// limits for any subagent role instead of the main-agent
// ReadWindowSize/ReadWindowThreshold. Originally scoped to just
// Explore/Plan, but "general-purpose" (the most commonly spawned
// subagent — the Agent tool's own catch-all for "researching complex
// questions, searching for code, and executing multi-step tasks") is
// built for the exact same kind of long, unsupervised read-heavy
// investigation and was still hitting the main agent's tight
// 10-reads-in-15-turns threshold and getting cut off mid-investigation.
const (
	exploreReadWindowSize      = 50
	exploreReadWindowThreshold = 40
)

// isPlanModeRequest detects the same thing the translator's own inline
// check does (ExitPlanMode declared among the request's tools) from raw
// request bytes, so callers that only have claudeBodyBytes, not the
// already-parsed/translated reqMap, can use the relaxed
// exploreReadWindow size (50) during Plan Mode too. An audit caught that
// both call sites hardcoded windowSize for "has modifying tools" at a
// flat 15-entry cap regardless of Plan Mode, while the trigger THRESHOLD
// checked in the translator correctly relaxed to 40 for Plan Mode — a
// 15-entry window summed against a 40 threshold can never fire, silently
// disabling the guard for the exact case (Read/Grep-heavy Plan Mode
// investigation) it exists to protect.
func isPlanModeRequest(claudeBodyBytes []byte) bool {
	isPlanMode := false
	gjson.GetBytes(claudeBodyBytes, "tools").ForEach(func(_, t gjson.Result) bool {
		if strings.EqualFold(t.Get("name").String(), "exitplanmode") {
			isPlanMode = true
			return false
		}
		return true
	})
	return isPlanMode
}

// hasModifyingToolsInRequest mirrors the translator's own hasModifyingTools
// computation (real declared tools, Plan-Mode-aware), as a raw-bytes
// equivalent for callers that only have claudeBodyBytes, not the parsed
// request map. An audit caught that the two readLoopLimits call sites
// were using !isPlanModeNow as a STAND-IN for this instead of checking
// the request's own declared tools — a main-agent session with
// genuinely no modifying tools declared (read-only review/exploration,
// outside Plan Mode) got hasModifyingTools=true from that proxy while
// the translator's OWN threshold computation correctly got false from
// the real tools list, pairing a 15-entry window with a 40-call
// threshold that can never be reached, silently disabling LoopGuard for
// exactly the read-heavy sessions it matters most for.
//
// GAP-P1-01: Claude Code declares Edit/Write on essentially every
// session regardless of task — a pure code-audit/review request still
// has them in "tools", so checking DECLARED tools alone makes this
// return true almost unconditionally, defeating the whole point of a
// relaxed exploratory threshold. What actually distinguishes a read-only
// investigation from an editing session is whether a modifying tool has
// actually been INVOKED yet, which the full message history (sent on
// every turn) already shows via past assistant tool_use blocks — so a
// session with Edit declared but never called still gets the relaxed
// window, and the very next turn after a real edit flips it back to
// strict, same as the translator's own progress-reset logic already
// keys off actual tool_use (see toolkind.IsModifyingToolName's own doc
// comment on the sibling read-streak-reset check).
func hasModifyingToolsInRequest(claudeBodyBytes []byte) bool {
	hasModifyingDeclared := false
	hasModifyingUsed := false
	isPlanMode := false
	gjson.GetBytes(claudeBodyBytes, "tools").ForEach(func(_, t gjson.Result) bool {
		name := t.Get("name").String()
		if toolkind.IsModifyingToolName(name) {
			hasModifyingDeclared = true
		}
		if strings.EqualFold(name, "exitplanmode") {
			isPlanMode = true
		}
		return true
	})
	if isPlanMode {
		return false
	}
	if !hasModifyingDeclared {
		return false
	}
	// Only consider recent assistant turns within ReadWindowSize (15) turns.
	// An edit made 20 turns ago shouldn't permanently restrict an agent doing
	// subsequent deep research or auditing.
	msgs := gjson.GetBytes(claudeBodyBytes, "messages").Array()
	startIdx := 0
	if len(msgs) > ReadWindowSize*2 {
		startIdx = len(msgs) - ReadWindowSize*2
	}
	for i := startIdx; i < len(msgs); i++ {
		m := msgs[i]
		if m.Get("role").String() != "assistant" {
			continue
		}
		m.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_use" && toolkind.IsModifyingToolName(block.Get("name").String()) {
				hasModifyingUsed = true
				return false
			}
			return true
		})
		if hasModifyingUsed {
			break
		}
	}
	return hasModifyingUsed
}

// readLoopLimits picks the LoopGuard read-loop window size and trip
// threshold for this turn's agent. agentKey is ParseAgentInfo's own output
// ("sub:<agentType>" for any subagent), so this needs no extra plumbing —
// the role is already encoded in the same string LoopGuard keys its state
// by. If hasModifyingTools is false (e.g. Plan Mode, or an investigation agent
// without edit permissions), we use the relaxed exploreReadWindow limits so
// legitimate file inspection does not trigger false-positive read-loop warnings.
func readLoopLimits(agentKey string, hasModifyingTools bool) (windowSize, threshold int) {
	// ParseAgentInfo's fallback path (request-class "subagent" but no
	// x-claude-code-agent-type header) returns the bare string "subagent"
	// with no "sub:" prefix at all.
	if strings.HasPrefix(agentKey, "sub:") || agentKey == "subagent" || !hasModifyingTools {
		return exploreReadWindowSize, exploreReadWindowThreshold
	}
	return ReadWindowSize, ReadWindowThreshold
}

// ReadLoopLimits is readLoopLimits exported for internal/translate's
// loop-guard nudge wiring (the only caller outside this package).
func ReadLoopLimits(agentKey string, hasModifyingTools bool) (windowSize, threshold int) {
	return readLoopLimits(agentKey, hasModifyingTools)
}

// IsPlanModeRequest exports isPlanModeRequest for internal/orchestrate,
// which needs the SAME Plan-Mode-aware threshold internal/translate's
// own loop-guard nudge wiring already uses (readLoopLimits), computed
// from raw claudeBodyBytes at the point a response has just come back.
func IsPlanModeRequest(claudeBodyBytes []byte) bool {
	return isPlanModeRequest(claudeBodyBytes)
}

// HasModifyingToolsInRequest exports hasModifyingToolsInRequest for
// internal/orchestrate — see that function's doc comment for why
// !isPlanModeNow is NOT an acceptable stand-in for this.
func HasModifyingToolsInRequest(claudeBodyBytes []byte) bool {
	return hasModifyingToolsInRequest(claudeBodyBytes)
}

// SyncPlanMode is the public entry point the translator calls BEFORE
// CheckAndResetReadStreak's threshold check, on the same request that may be
// the first to exit Plan Mode — see syncPlanModeLocked's doc comment (manager.go)
// for why waiting for RecordReadTurnWithRealProgress (which only runs after this
// SAME request's response comes back) was one request too late.
func (lg *LoopGuardManager) SyncPlanMode(agentKey string, isPlanMode bool) {
	if agentKey == "" || agentKey == "auxiliary" || strings.HasPrefix(agentKey, "auxiliary|") {
		return
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()
	lg.touchLocked(agentKey)
	lg.syncPlanModeLocked(agentKey, isPlanMode)
}
