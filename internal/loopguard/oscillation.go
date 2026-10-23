// Part of the loopguard package's port from superrelay (v1)'s
// loopguard.go — see bashdetect.go for the split rationale. This file
// holds repeated-tool-call (oscillation) detection.
package loopguard

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// ResetToolCallHistory clears recorded tool call history for agentKey.
func (lg *LoopGuardManager) ResetToolCallHistory(agentKey string) {
	if agentKey == "" {
		return
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()
	lg.toolCallHist[agentKey] = nil
}

// RecordToolCalls records tool calls executed in this turn.
func (lg *LoopGuardManager) RecordToolCalls(agentKey string, calls []string, hasIgnoreTag bool) {
	if agentKey == "" || agentKey == "auxiliary" || strings.HasPrefix(agentKey, "auxiliary|") {
		return
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()
	lg.touchLocked(agentKey)

	if hasIgnoreTag {
		lg.toolCallHist[agentKey] = nil
		return
	}

	if len(calls) == 0 {
		return
	}

	// Canonical sorted hash of calls in this turn
	sort.Strings(calls)
	h := sha256.New()
	for _, c := range calls {
		h.Write([]byte(c))
		h.Write([]byte{';'})
	}
	turnHash := hex.EncodeToString(h.Sum(nil))

	hist := lg.toolCallHist[agentKey]
	hist = append(hist, turnHash)
	if len(hist) > ToolCallHistMax {
		hist = hist[len(hist)-ToolCallHistMax:]
	}
	lg.toolCallHist[agentKey] = hist
}

func (lg *LoopGuardManager) CheckAndResetOscillation(agentKey string) bool {
	if agentKey == "" || agentKey == "auxiliary" || strings.HasPrefix(agentKey, "auxiliary|") {
		return false
	}
	lg.mu.Lock()
	defer lg.mu.Unlock()

	hist := lg.toolCallHist[agentKey]
	n := len(hist)

	// Detect period 1 (repeated x3), period 2 (A->B repeated x3), period 3 (A->B->C repeated x2)
	patterns := []struct {
		period int
		reps   int
	}{
		{1, 3},
		{2, 3},
		{3, 2},
	}

	for _, p := range patterns {
		need := p.period * p.reps
		if n < need {
			continue
		}
		window := hist[n-need:]
		pat := window[:p.period]

		// check non-degenerate pattern
		isDegenerate := false
		if p.period > 1 {
			unique := make(map[string]bool)
			for _, item := range pat {
				unique[item] = true
			}
			if len(unique) < p.period {
				isDegenerate = true
			}
		}
		if isDegenerate {
			continue
		}

		matched := true
		for i := 0; i < need; i++ {
			if window[i] != pat[i%p.period] {
				matched = false
				break
			}
		}

		if matched {
			lg.toolCallHist[agentKey] = nil
			return true
		}
	}

	return false
}
