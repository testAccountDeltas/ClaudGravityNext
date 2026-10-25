package stream

import (
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/loopguard"
	"claudgravity/internal/reqctx"
)

// A paged Read is widened on the wire from the very first call, with no
// nudge and no instruction file involved; offset and path stay as asked.
func TestHandleFunctionCall_WidensPagedReads(t *testing.T) {
	p := &reqctx.Params{ModelName: "claude-opus-4-5", AgentKey: "main", SessionID: "sess-widen", InstanceID: "inst-widen"}

	f, rec, _ := heldFSM(p)
	if err := f.HandleFunctionCall("Read", `{"file_path":"/big.md","offset":200,"limit":100}`, ""); err != nil {
		t.Fatal(err)
	}
	got := covToolDeltas(rec.Body.String())
	if len(got) != 1 || gjson.Get(got[0], "limit").Int() != loopguard.WidenedReadLimit || gjson.Get(got[0], "offset").Int() != 200 {
		t.Fatalf("the paged read must be widened: %v", got)
	}
}
