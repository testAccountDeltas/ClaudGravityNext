// Part of the loopguard package — see bashdetect.go for the split
// rationale. Models (Gemini above all) page through a big file in 100-line
// slices and ignore every "read more per call" hint: observed, 38
// consecutive 100-line Reads of one file, context 24k -> 124k tokens. A
// project's instruction file cannot be relied on to fix that (the next
// project will not have it), so the gateway enforces it on the wire: every
// Read the model emits with a small limit is widened before it reaches the
// client, and the same pass over the file takes a fraction of the calls.
package loopguard

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type widenedRange struct {
	path  string
	start int64
}

// WidenedReadLimit is the minimum line count of a Read that has a limit. It
// stays well under Read's own output cap (about 25k tokens): at the typical
// 25-30 tokens per source line that is 20-24k tokens at worst, and a dense
// file is handled by the retry rule in WidenPagedRead.
const WidenedReadLimit = 800

// minWidenedReadLimit is the floor the adaptive shrink in NoteReadTooLarge
// stops at.
const minWidenedReadLimit = 200

// widenLimitLocked is the size loopKey's paged Reads are widened to. Caller
// must hold lg.mu.
func (lg *LoopGuardManager) widenLimitLocked(loopKey string) int64 {
	if n, ok := lg.widenLimit[loopKey]; ok {
		return n
	}
	return WidenedReadLimit
}

// NoteReadTooLarge records that a Read of loopKey came back with "exceeds
// maximum allowed tokens" (a dense file: long lines make 800 lines more than
// the ~25k-token cap). The widened size for this agent is halved, down to
// minWidenedReadLimit, so the next paged Read does not fail the same way.
func (lg *LoopGuardManager) NoteReadTooLarge(loopKey string) {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	next := lg.widenLimitLocked(loopKey) / 2
	if next < minWidenedReadLimit {
		next = minWidenedReadLimit
	}
	if lg.widenLimit == nil {
		lg.widenLimit = make(map[string]int64)
	}
	lg.widenLimit[loopKey] = next
	lg.touchLocked(loopKey)
}

// WidenPagedRead returns argsRaw with a small "limit" raised to
// WidenedReadLimit when toolName is Read. It reports whether it changed
// anything. offset and file_path are never touched.
//
// The call is left alone when:
//   - no limit was given (Read's default already covers up to 2000 lines);
//   - the limit is already >= WidenedReadLimit;
//   - the request starts inside the range this agent's previous call to the
//     same file was widened to: either the model is retrying after Read
//     refused the larger slice (output cap), where widening again would just
//     repeat the error, or it is re-reading lines it was already given.
//
// loopKey only scopes that retry memory; an empty key still widens.
func (lg *LoopGuardManager) WidenPagedRead(loopKey, toolName, argsRaw string) (string, bool) {
	if !strings.EqualFold(toolName, "read") {
		return argsRaw, false
	}
	limit := gjson.Get(argsRaw, "limit")
	if !limit.Exists() || limit.Type != gjson.Number || limit.Int() <= 0 || limit.Int() >= WidenedReadLimit {
		return argsRaw, false
	}
	path := gjson.Get(argsRaw, "file_path").String()
	if path == "" {
		return argsRaw, false
	}
	offset := gjson.Get(argsRaw, "offset").Int()

	lg.mu.Lock()
	defer lg.mu.Unlock()
	target := lg.widenLimitLocked(loopKey)
	if limit.Int() >= target {
		return argsRaw, false
	}
	if prev, ok := lg.lastWidened[loopKey]; ok && prev.path == path && offset >= prev.start && offset < prev.start+target {
		return argsRaw, false
	}
	widened, err := sjson.Set(argsRaw, "limit", target)
	if err != nil {
		return argsRaw, false
	}
	if lg.lastWidened == nil {
		lg.lastWidened = make(map[string]widenedRange)
	}
	lg.lastWidened[loopKey] = widenedRange{path: path, start: offset}
	if loopKey != "" {
		lg.touchLocked(loopKey)
	}
	return widened, true
}
