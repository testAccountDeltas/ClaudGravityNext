package stream

import (
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/loopguard"
	"claudgravity/internal/reqctx"
)

// A model that keeps paging a file after the read-loop nudge gets its next
// Read widened on the wire; before the nudge it is passed through as asked.
func TestHandleFunctionCall_WidensPagedReadsOnlyAfterTheNudge(t *testing.T) {
	p := &reqctx.Params{ModelName: "claude-opus-4-5", AgentKey: "main", SessionID: "sess-widen", InstanceID: "inst-widen"}
	key := "main|sess-widen|inst-widen"
	args := `{"file_path":"/big.md","offset":100,"limit":100}`

	f, rec, _ := heldFSM(p)
	if err := f.HandleFunctionCall("Read", args, ""); err != nil {
		t.Fatal(err)
	}
	if got := covToolDeltas(rec.Body.String()); len(got) != 1 || gjson.Get(got[0], "limit").Int() != 100 {
		t.Fatalf("before the nudge the call is untouched: %v", got)
	}

	lg := loopguard.Global()
	for i := 0; i < 10; i++ {
		lg.RecordReadTurnWithRealProgress(key, 1, false, false, 15, false)
	}
	if lg.CheckAndResetReadStreak(key, 10) != 1 {
		t.Fatal("setup: the soft nudge should have fired")
	}

	f, rec, _ = heldFSM(p)
	if err := f.HandleFunctionCall("Read", `{"file_path":"/big.md","offset":200,"limit":100}`, ""); err != nil {
		t.Fatal(err)
	}
	got := covToolDeltas(rec.Body.String())
	if len(got) != 1 || gjson.Get(got[0], "limit").Int() != loopguard.WidenedReadLimit || gjson.Get(got[0], "offset").Int() != 200 {
		t.Fatalf("the paged read must be widened: %v", got)
	}
}
