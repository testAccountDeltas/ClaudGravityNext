package stream

import (
	"testing"

	"claudgravity/internal/reqctx"
)

// A request that switched function calling off (tool_choice "none", a label
// request) must not get a tool_use even when Gemini emits a functionCall.
func TestHandleFunctionCall_DroppedWhenToolCallsAreForbidden(t *testing.T) {
	p := &reqctx.Params{ModelName: "claude-opus-4-5", AgentKey: "main", SessionID: "s", InstanceID: "i", SuppressToolCalls: true}
	f, rec, _ := heldFSM(p)
	if err := f.HandleFunctionCall("Read", `{"file_path":"/a.go"}`, ""); err != nil {
		t.Fatal(err)
	}
	if got := covToolDeltas(rec.Body.String()); len(got) != 0 {
		t.Fatalf("a forbidden call reached the client: %v", got)
	}
	if f.toolUseOccurred {
		t.Fatal("the dropped call must not flip the turn into a tool_use turn")
	}

	p.SuppressToolCalls = false
	f, rec, _ = heldFSM(p)
	if err := f.HandleFunctionCall("Read", `{"file_path":"/a.go"}`, ""); err != nil {
		t.Fatal(err)
	}
	if got := covToolDeltas(rec.Body.String()); len(got) != 1 {
		t.Fatalf("an ordinary request keeps its call: %v", got)
	}
}
