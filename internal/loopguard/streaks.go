// Part of the loopguard package's port from superrelay (v1)'s
// loopguard.go — see bashdetect.go for the split rationale. This file
// holds the read-streak recording and threshold-checking methods.
package loopguard

import "strings"

// RecordReadTurn appends this turn's Read-tool-call count to agentKey's
// sliding window. madeProgress (a Write/Edit happened, or the turn carried a
// substantial amount of actual explanatory text/thinking — not just "let me
// check X") wipes the window: that's real forward motion, not skimming.
func (lg *LoopGuardManager) RecordReadTurn(agentKey string, readCount int, madeProgress bool, windowSize int, isPlanMode bool) {
	lg.RecordReadTurnWithRealProgress(agentKey, readCount, madeProgress, madeProgress, windowSize, isPlanMode)
}

// RecordReadTurnWithRealProgress separates "this turn resets the read-count
// window" (madeProgress, which includes a zero-tool-call turn with a
// substantial text reply — reasonably treated as real progress for
// deciding whether to detect a loop at all) from "this turn earns a clean
// slate on the NUDGE ESCALATION WORDING too" (realProgress — an audit
// caught these being conflated under one flag broke the exact escalation
// CheckAndResetReadStreak's own doc comment describes: a model that
// politely ACKNOWLEDGES a soft nudge with a long text reply but makes no
// Write/Edit, then immediately resumes reading, previously reset
// nudgeCount via that acknowledgment alone — the second read-loop
// occurrence fired the same soft wording again instead of escalating,
// even though nothing was actually fixed between the two. Only genuine
// Write/Edit progress should earn that clean slate; a text-only turn
// still resets the detection WINDOW (so unrelated activity doesn't
// falsely accumulate), just not the escalation level.
func (lg *LoopGuardManager) RecordReadTurnWithRealProgress(agentKey string, readCount int, madeProgress, realProgress bool, windowSize int, isPlanMode bool) {
	if agentKey == "" || agentKey == "auxiliary" || strings.HasPrefix(agentKey, "auxiliary|") {
		return
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()
	lg.touchLocked(agentKey)

	// A follow-up audit caught that the Plan-Mode transition clear below
	// used to be the ONLY place this ran, but this function is only ever
	// called AFTER Gemini's response for the CURRENT request comes back —
	// the translator's own CheckAndResetReadStreak call, which decides
	// whether to inject a nudge into the OUTGOING request, runs strictly
	// BEFORE that, while translating the request. On the exact request
	// that exits Plan Mode, the threshold check still read the stale,
	// not-yet-cleared Plan-Mode-era window (because the clear hadn't
	// happened yet for THIS request), nudging an agent that hadn't
	// looped on anything in real-execution mode at all. Extracted into
	// syncPlanModeLocked so the translator can call the public
	// SyncPlanMode wrapper before the threshold check, on the SAME
	// request — idempotent, so this function calling it again after the
	// response arrives is a harmless no-op once the transition's already
	// been applied.
	lg.syncPlanModeLocked(agentKey, isPlanMode)

	if madeProgress {
		delete(lg.readWindows, agentKey)
		if realProgress {
			delete(lg.nudgeCount, agentKey) // genuine progress earns a clean slate for the nudge wording too
		}
		return
	}

	w := append(lg.readWindows[agentKey], readCount)
	if len(w) > windowSize {
		w = w[len(w)-windowSize:]
	}
	lg.readWindows[agentKey] = w
}

// CheckAndResetReadStreak reports the nudge's escalation level: 0 means no
// nudge fires. 1 is the first (soft) warning for this agent since its last
// genuine Write/Edit; 2+ means the soft nudge already fired and the agent
// slid straight back into another full read-loop window without ever
// writing/editing in between — confirmed real pattern (the model politely
// acknowledges the soft nudge, then immediately reads again), so the second
// firing gets much more directive wording instead of repeating the same
// whisper indefinitely.
// CheckAndResetReadStreak takes an explicit threshold instead of always
// using the package-level ReadWindowThreshold: a research subagent
// (Explore/Plan) is deliberately built to read 20-40+ files in a row with
// no Write/Edit at all — that's its entire job, not a loop — so the same
// 10-read threshold tuned for the main agent was cutting these subagents
// off mid-investigation and forcing them to return an incomplete answer.
// See readLoopLimits (planmode.go), which picks the threshold passed in here
// based on the agent's role.
func (lg *LoopGuardManager) CheckAndResetReadStreak(agentKey string, threshold int) int {
	if agentKey == "" || agentKey == "auxiliary" || strings.HasPrefix(agentKey, "auxiliary|") {
		return 0
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()

	total := 0
	for _, c := range lg.readWindows[agentKey] {
		total += c
	}
	if total >= threshold {
		delete(lg.readWindows, agentKey)
		lg.nudgeCount[agentKey]++
		return lg.nudgeCount[agentKey]
	}
	return 0
}
