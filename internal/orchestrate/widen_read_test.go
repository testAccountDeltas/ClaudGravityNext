package orchestrate

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/loopguard"
)

func readCall(args string) string {
	return tfWrap(`{"content":{"role":"model","parts":[{"functionCall":{"name":"Read","args":` + args + `}}]},"finishReason":"STOP"}`)
}

// The non-streaming path widens a paged Read the same way the streaming FSM
// does, once the agent's read-loop nudge has fired.
func TestNonStreaming_WidensAPagedReadAfterTheNudge(t *testing.T) {
	key := "|sess-" + t.Name() + "|"
	lg := loopguard.Global()
	for i := 0; i < 10; i++ {
		lg.RecordReadTurnWithRealProgress(key, 1, false, false, 15, false)
	}
	if lg.CheckAndResetReadStreak(key, 10) != 1 {
		t.Fatal("setup: nudge should fire")
	}
	claude := `{"model":"claude-sonnet-4-5","max_tokens":1024,"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}}}}],"messages":[{"role":"user","content":"go on"}]}`
	r := runExec(t, execOpts{nAcc: 1, claude: claude,
		up: func(int, *http.Request, string) (*http.Response, error) {
			return respBody(200, readCall(`{"file_path":"/big.md","offset":300,"limit":100}`)), nil
		}})
	if !r.ok {
		t.Fatalf("status %d err %v", r.status, r.err)
	}
	in := gjson.Get(r.body, "content.#(type==\"tool_use\").input")
	if !in.Exists() || in.Get("limit").Int() != loopguard.WidenedReadLimit || in.Get("offset").Int() != 300 {
		t.Fatalf("tool_use input = %s\n%s", in.Raw, strings.TrimSpace(r.body))
	}
}
