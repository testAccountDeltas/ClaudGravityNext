package stream

import (
	"strings"
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

func TestDroppedCallsAreCounted(t *testing.T) {
	p := &reqctx.Params{ModelName: "claude-opus-4-5", AgentKey: "main", SessionID: "s", InstanceID: "i", SuppressToolCalls: true}
	f, _, _ := heldFSM(p)
	_ = f.HandleFunctionCall("Read", `{}`, "")
	_ = f.HandleFunctionCall("Bash", `{}`, "")
	if f.droppedToolCalls != 2 {
		t.Fatalf("droppedToolCalls = %d", f.droppedToolCalls)
	}
}

// A streamed turn whose only product is a call the request forbids ends as a
// clean, empty end_turn: no UpstreamTurnError (which would be retried on every
// account), no tool_use.
func TestStream_OnlyAForbiddenCallEndsCleanly(t *testing.T) {
	p := basicParams()
	p.SuppressToolCalls = true
	f, rec, _ := heldFSM(p)
	if err := f.ProcessGeminiChunk(chunk(`{"functionCall":{"name":"Read","args":{"file_path":"/a.go"}}}`, "STOP")); err != nil {
		t.Fatalf("expected a clean finish, got %v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "tool_use") || !strings.Contains(body, `"stop_reason":"end_turn"`) || !strings.Contains(body, "message_stop") {
		t.Fatalf("unexpected stream:\n%s", body)
	}
}
