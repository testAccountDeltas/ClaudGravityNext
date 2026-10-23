// Part of the orchestrate package — see pipeline.go for the package doc
// comment. Split out of streaming.go purely to stay under the 400-line
// ceiling: the post-stream LoopGuard bookkeeping.
package orchestrate

import (
	"claudgravity/internal/loopguard"
	"claudgravity/internal/reqctx"
	"claudgravity/internal/stream"
	"claudgravity/internal/toolkind"
)

// recordStreamLoopGuard feeds one finished streaming turn into LoopGuard's
// read-loop and tool-oscillation detectors.
func recordStreamLoopGuard(p *reqctx.Params, stats stream.Stats, claudeBodyBytes []byte) {
	readCount := 0
	madeProgress := false
	for _, sig := range stats.ToolCallSignatures {
		name, isReadEquiv, isWriteEquiv := loopguard.ReadLoopEquivalentToolName(sig)
		if isReadEquiv {
			readCount++
		} else if toolkind.IsModifyingToolName(name) || isWriteEquiv {
			madeProgress = true
		}
	}
	realProgress := madeProgress
	if readCount == 0 && !madeProgress {
		// GAP-TH-11: a turn with a long chain of reasoning (e.g. 10,000
		// chars of thinking) but a short visible answer was counted as
		// "no progress" toward the read-loop counter, judged on
		// TextChars alone — substantial reasoning is real work, not a
		// stalled read-loop, even when the visible answer is short.
		madeProgress = stats.TextChars > 200 || stats.ThinkingChars > 2000
	}
	loopKey := p.AgentKey + "|" + p.SessionID + "|" + p.InstanceID
	isPlanModeNow := loopguard.IsPlanModeRequest(claudeBodyBytes)
	readWindowSize, _ := loopguard.ReadLoopLimits(p.AgentKey, loopguard.HasModifyingToolsInRequest(claudeBodyBytes))
	loopguard.Global().RecordReadTurnWithRealProgress(loopKey, readCount, madeProgress, realProgress, readWindowSize, isPlanModeNow)

	hasIgnoreTag := loopguard.HasIgnoreLoopTag(stats.LastText) || loopguard.HasIgnoreLoopTag(stats.LastThinking) || loopguard.HasIgnoreLoopTag(string(claudeBodyBytes))
	// Status-check polls repeated while waiting on async work are exempt from oscillation detection.
	oscillationCalls := make([]string, 0, len(stats.ToolCallSignatures))
	for _, sig := range stats.ToolCallSignatures {
		if !loopguard.IsStatusCheckToolCall(sig) {
			oscillationCalls = append(oscillationCalls, sig)
		}
	}
	loopguard.Global().RecordToolCalls(loopKey, oscillationCalls, hasIgnoreTag)
}
