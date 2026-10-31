package stream

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/loopguard"
	"claudgravity/internal/reqctx"
)

// widenSeq gives each run its own file path: the loop guard remembers, per
// agent, the range it last widened, so a repeated run (-count=2) on the same
// path would be treated as a retry and left alone.
var widenSeq atomic.Int64

// A paged Read is widened on the wire from the very first call, with no
// nudge and no instruction file involved; offset and path stay as asked.
func TestHandleFunctionCall_WidensPagedReads(t *testing.T) {
	p := &reqctx.Params{ModelName: "claude-opus-4-5", AgentKey: "main", SessionID: "sess-widen", InstanceID: "inst-widen"}

	f, rec, _ := heldFSM(p)
	if err := f.HandleFunctionCall("Read", fmt.Sprintf(`{"file_path":"/big-%d.md","offset":200,"limit":100}`, widenSeq.Add(1)), ""); err != nil {
		t.Fatal(err)
	}
	got := covToolDeltas(rec.Body.String())
	if len(got) != 1 || gjson.Get(got[0], "limit").Int() != loopguard.WidenedReadLimit || gjson.Get(got[0], "offset").Int() != 200 {
		t.Fatalf("the paged read must be widened: %v", got)
	}
}
