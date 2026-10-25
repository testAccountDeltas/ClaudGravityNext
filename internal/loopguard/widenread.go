// Part of the loopguard package — see bashdetect.go for the split
// rationale. A read-loop nudge is only a request: a model that is paging
// through one big file in 100-line slices routinely acknowledges it and
// keeps paging (observed: 38 consecutive 100-line Reads of the same file,
// context 24k -> 124k tokens, three nudges ignored). Once the nudge has
// fired for an agent, this file makes the gateway enforce what the nudge
// asked for: a Read the model emits with a small limit is widened before it
// reaches the client, so the same pass over the file takes a fraction of the
// calls.
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

// WidenedReadLimit is the line count a paged Read is widened to. It stays
// well under Read's own output cap (about 25k tokens): at the typical
// 25-30 tokens per source line that is 12-15k tokens, and a dense file is
// handled by the retry rule in WidenPagedRead.
const WidenedReadLimit = 500

// WidenPagedRead returns argsRaw with a small "limit" raised to
// WidenedReadLimit when toolName is Read and loopKey's read-loop nudge has
// already fired (and no Write/Edit has happened since). It reports whether
// it changed anything.
//
// The call is left alone when:
//   - the agent has not been nudged (a targeted read stays exactly as asked);
//   - no limit was given (Read's default already covers up to 2000 lines);
//   - the limit is already >= WidenedReadLimit;
//   - the request starts inside the range this agent's previous call to the
//     same file was widened to: either the model is retrying after Read
//     refused the larger slice (output cap), where widening again would just
//     repeat the error, or it is re-reading lines it was already given.
func (lg *LoopGuardManager) WidenPagedRead(loopKey, toolName, argsRaw string) (string, bool) {
	if loopKey == "" || !strings.EqualFold(toolName, "read") {
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
	if lg.nudgeCount[loopKey] == 0 {
		return argsRaw, false
	}
	if prev, ok := lg.lastWidened[loopKey]; ok && prev.path == path && offset >= prev.start && offset < prev.start+WidenedReadLimit {
		return argsRaw, false
	}
	widened, err := sjson.Set(argsRaw, "limit", WidenedReadLimit)
	if err != nil {
		return argsRaw, false
	}
	if lg.lastWidened == nil {
		lg.lastWidened = make(map[string]widenedRange)
	}
	lg.lastWidened[loopKey] = widenedRange{path: path, start: offset}
	lg.touchLocked(loopKey)
	return widened, true
}
