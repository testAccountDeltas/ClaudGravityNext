// Part of the loopguard package's port from superrelay (v1)'s
// loopguard.go — see bashdetect.go for the split rationale. This file
// holds the LoopGuardManager type, its sweep lifecycle, and
// RecordEditResult.
package loopguard

import (
	"regexp"
	"sync"
	"time"
)

var oscIgnoreTagRegex = regexp.MustCompile(`(?i)\[ignoring loop detection\]`)

// HasIgnoreLoopTag reports whether s contains the ignore-loop-detection tag (case-insensitive).
func HasIgnoreLoopTag(s string) bool {
	return oscIgnoreTagRegex.MatchString(s)
}

// StripIgnoreLoopTag removes all case-insensitive occurrences of the ignore tag from s.
func StripIgnoreLoopTag(s string) string {
	return oscIgnoreTagRegex.ReplaceAllString(s, "")
}

const (
	// ReadWindowSize/ReadWindowThreshold replace a strict "N consecutive
	// pure-Read turns" counter: measured live (84 subagent requests, one
	// real Explore session), Gemini rarely does 10 single-Read turns in an
	// unbroken row — it interleaves Bash or batches 2-4 Reads per turn — so
	// the old streak-based counter never fired even with 44 total Read
	// calls scattered across the session. This instead sums Read calls
	// over a sliding window of recent turns regardless of what's
	// interspersed, and only resets on genuine progress (Write/Edit, or a
	// substantial explanation) rather than on every non-Read turn.
	ReadWindowSize      = 15
	ReadWindowThreshold = 10
	ToolCallHistMax     = 12
	OscIgnoreTag        = "[ignoring loop detection]"

	// loopGuardStaleAfter/loopGuardSweepEvery bound the otherwise-unbounded
	// growth of readWindows/toolCallHist/lastSeen across the many distinct,
	// ephemeral agentKeys a long-running gateway process accumulates.
	loopGuardStaleAfter = 2 * time.Hour
	loopGuardSweepEvery = 30 * time.Minute
)

type LoopGuardManager struct {
	mu            sync.Mutex
	readWindows   map[string][]int     // agentKey -> recent per-turn Read-tool-call counts (sliding, max ReadWindowSize)
	toolCallHist  map[string][]string  // agentKey -> list of turn call hashes
	lastSeen      map[string]time.Time // agentKey -> last activity, for staleness sweeps
	nudgeCount    map[string]int       // agentKey -> how many read-loop nudges fired since the last genuine Write/Edit; escalates wording instead of staying a one-time whisper
	editFailures  map[string]int       // agentKey -> consecutive Edit tool_result is_error:true count
	lastPlanMode  map[string]bool      // agentKey -> whether the last-recorded turn was under Plan Mode's lenient window/threshold
	pendingNudges map[string][]string  // agentKey/sessionKey -> pending gateway notices to inject into system_instruction
}

var globalLoopGuard = &LoopGuardManager{
	readWindows:   make(map[string][]int),
	toolCallHist:  make(map[string][]string),
	lastSeen:      make(map[string]time.Time),
	nudgeCount:    make(map[string]int),
	editFailures:  make(map[string]int),
	lastPlanMode:  make(map[string]bool),
	pendingNudges: make(map[string][]string),
}

// Global exposes the package-level singleton to callers outside this
// package (internal/translate's loop-guard nudge wiring, the future
// internal/orchestrate) — same package-level-singleton-plus-accessor
// convention as internal/accounts.ModelCatalog().
func Global() *LoopGuardManager { return globalLoopGuard }

// RecordEditResult tracks consecutive Edit tool_result failures per agent —
// the response-side madeProgress signal counts ANY Edit call as progress
// regardless of outcome (it can't know the outcome yet; the client
// reports is_error on the client's own NEXT request, in that request's
// tool_result, not in this gateway's response to the call that failed),
// so a model stuck retrying the exact same failing old_string indefinitely
// never tripped the read-loop guard. Returns the new consecutive-failure
// count (0 if this call was a success/reset it).
func (lg *LoopGuardManager) RecordEditResult(agentKey string, isError bool) int {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	lg.touchLocked(agentKey)
	if isError {
		lg.editFailures[agentKey]++
		return lg.editFailures[agentKey]
	}
	lg.editFailures[agentKey] = 0
	return 0
}

// RecordPendingNudge stores a gateway notice for a session/loop key, to be
// injected into system_instruction parts instead of corrupting tool_result bodies.
func (lg *LoopGuardManager) RecordPendingNudge(key, text string) {
	if key == "" || text == "" {
		return
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()
	lg.touchLocked(key)
	if lg.pendingNudges == nil {
		lg.pendingNudges = make(map[string][]string)
	}
	for _, existing := range lg.pendingNudges[key] {
		if existing == text {
			return
		}
	}
	lg.pendingNudges[key] = append(lg.pendingNudges[key], text)
}

// TakePendingNudges returns and clears all pending notices for key.
func (lg *LoopGuardManager) TakePendingNudges(key string) []string {
	if key == "" {
		return nil
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()
	if lg.pendingNudges == nil {
		return nil
	}
	nudges := lg.pendingNudges[key]
	delete(lg.pendingNudges, key)
	return nudges
}

func init() {
	go func() {
		ticker := time.NewTicker(loopGuardSweepEvery)
		defer ticker.Stop()
		for range ticker.C {
			globalLoopGuard.sweepStale()
		}
	}()
}

func (lg *LoopGuardManager) sweepStale() {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	cutoff := time.Now().Add(-loopGuardStaleAfter)
	for k, t := range lg.lastSeen {
		if t.Before(cutoff) {
			delete(lg.lastSeen, k)
			delete(lg.readWindows, k)
			delete(lg.toolCallHist, k)
			delete(lg.nudgeCount, k)
			delete(lg.editFailures, k)
			delete(lg.lastPlanMode, k)
			delete(lg.pendingNudges, k)
		}
	}
}

// touchLocked marks agentKey as active; caller must hold lg.mu.
func (lg *LoopGuardManager) touchLocked(agentKey string) {
	lg.lastSeen[agentKey] = time.Now()
}

// syncPlanModeLocked clears agentKey's read window on a Plan-Mode true→false
// transition (lenient window=40 built while planning can't be fairly judged
// against the strict window=10 used once real execution starts) and records
// the new state. Caller must hold lg.mu. Idempotent: once lastPlanMode is
// updated to isPlanMode, calling this again with the same value is a no-op.
func (lg *LoopGuardManager) syncPlanModeLocked(agentKey string, isPlanMode bool) {
	wasPlanMode, seen := lg.lastPlanMode[agentKey]
	if seen && wasPlanMode && !isPlanMode {
		delete(lg.readWindows, agentKey)
		// An audit caught nudgeCount was left intact on this same
		// transition — a Plan-Mode-era nudge (nudgeCount==1) survived
		// into execution, so the FIRST real read-streak during execution
		// incremented it to 2 and immediately escalated to "you were
		// already warned" wording despite never actually warning the
		// agent during execution itself.
		delete(lg.nudgeCount, agentKey)
	}
	lg.lastPlanMode[agentKey] = isPlanMode
}
