package orchestrate

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/loopguard"
)

// widenSeq gives each run its own file path: the loop guard remembers, per
// agent, the range it last widened, so a repeated run (-count=2) on the same
// path would be treated as a retry and left alone.
var widenSeq atomic.Int64

func readCall(args string) string {
	return tfWrap(`{"content":{"role":"model","parts":[{"functionCall":{"name":"Read","args":` + args + `}}]},"finishReason":"STOP"}`)
}

// The non-streaming path widens a paged Read the same way the streaming FSM
// does.
func TestNonStreaming_WidensAPagedRead(t *testing.T) {
	path := fmt.Sprintf("/big-%d.md", widenSeq.Add(1))
	claude := `{"model":"claude-sonnet-4-5","max_tokens":1024,"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}}}}],"messages":[{"role":"user","content":"go on"}]}`
	r := runExec(t, execOpts{nAcc: 1, claude: claude,
		up: func(int, *http.Request, string) (*http.Response, error) {
			return respBody(200, readCall(fmt.Sprintf(`{"file_path":%q,"offset":300,"limit":100}`, path))), nil
		}})
	if !r.ok {
		t.Fatalf("status %d err %v", r.status, r.err)
	}
	in := gjson.Get(r.body, "content.#(type==\"tool_use\").input")
	if !in.Exists() || in.Get("limit").Int() != loopguard.WidenedReadLimit || in.Get("offset").Int() != 300 {
		t.Fatalf("tool_use input = %s\n%s", in.Raw, strings.TrimSpace(r.body))
	}
}
