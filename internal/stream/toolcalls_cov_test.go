package stream

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/reqctx"
)

func covToolDeltas(body string) []string {
	var out []string
	for _, e := range parseEvents(body) {
		if gjson.Get(e.Data, "delta.type").String() == "input_json_delta" {
			out = append(out, gjson.Get(e.Data, "delta.partial_json").String())
		}
	}
	return out
}

func TestCovDisableParallelToolUseDropsSecondCall(t *testing.T) {
	p := &reqctx.Params{ModelName: "claude-opus-4-5", DisableParallelToolUse: true}
	f, rec, _ := heldFSM(p)
	if err := f.HandleFunctionCall("Read", `{"file_path":"a"}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.HandleFunctionCall("Read", `{"file_path":"b"}`, ""); err != nil {
		t.Fatal(err)
	}
	got := covToolDeltas(rec.Body.String())
	if len(got) != 1 || !strings.Contains(got[0], `"a"`) {
		t.Fatalf("only the first call may stream, got %v", got)
	}
	if len(f.toolNames) != 1 {
		t.Fatalf("toolNames = %v", f.toolNames)
	}
}

func TestCovSyntheticReasonStrippedFromArgs(t *testing.T) {
	p := &reqctx.Params{ModelName: "claude-opus-4-5", SyntheticReasonTools: map[string]bool{"NoArgs": true}}
	f, rec, _ := heldFSM(p)
	if err := f.HandleFunctionCall("NoArgs", `{"reason":"why not"}`, ""); err != nil {
		t.Fatal(err)
	}
	got := covToolDeltas(rec.Body.String())
	if len(got) != 1 || strings.Contains(got[0], "reason") {
		t.Fatalf("synthetic reason must be stripped, got %v", got)
	}
}

func TestCovParallelCallsBothStreamByDefault(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	_ = f.HandleFunctionCall("Read", `{"file_path":"a"}`, "")
	_ = f.HandleFunctionCall("Read", `{"file_path":"b"}`, "")
	if got := covToolDeltas(rec.Body.String()); len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if len(f.toolCallIDs) != 2 || f.toolCallIDs[0] == f.toolCallIDs[1] {
		t.Fatalf("ids must be distinct: %v", f.toolCallIDs)
	}
}

func TestCovGrepAndGlobRepairPathsRun(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	if err := f.HandleFunctionCall("Grep", `{"pattern":"foo"}`, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.HandleFunctionCall("glob", `{"pattern":"*.go"}`, ""); err != nil {
		t.Fatal(err)
	}
	got := covToolDeltas(rec.Body.String())
	if len(got) != 2 || gjson.Get(got[0], "pattern").String() != "foo" || gjson.Get(got[1], "pattern").String() != "*.go" {
		t.Fatalf("got %v", got)
	}
}

func TestCovToolCallEmitsPrefillAndFlushesHeldText(t *testing.T) {
	p := &reqctx.Params{ModelName: "claude-opus-4-5", PrefillText: "{", StopSequences: []string{"STOPHERE"}, MaxTokens: 100}
	f, rec, _ := heldFSM(p)
	f.textHoldBuffer = "held"
	if err := f.HandleFunctionCall("Read", `{"file_path":"a"}`, "SIGZ"); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if !f.prefillEmitted {
		t.Fatal("prefill must be emitted before a leading tool call")
	}
	if !strings.Contains(textOf(body), "{") {
		t.Fatalf("prefill text missing: %q", textOf(body))
	}
	names := blockSummary(body)
	if len(names) != 2 || names[0] != "text" || names[1] != "tool_use:Read" {
		t.Fatalf("blocks = %v", names)
	}
	if f.textHoldBuffer != "" {
		t.Fatalf("hold buffer must be flushed: %q", f.textHoldBuffer)
	}
	if got := getToolSignature(f.toolCallIDs[0]); got != "SIGZ" {
		t.Fatalf("tool signature = %q", got)
	}
}

func TestCovToolCallInheritsOpenThinkingSignature(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	if err := f.ProcessGeminiChunk(chunk(`{"thought":true,"text":"plan"}`, "")); err != nil {
		t.Fatal(err)
	}
	f.thoughtSignature = "OPENSIG"
	if err := f.HandleFunctionCall("Read", `{"file_path":"a"}`, ""); err != nil {
		t.Fatal(err)
	}
	if got := getToolSignature(f.toolCallIDs[0]); got != "OPENSIG" {
		t.Fatalf("tool signature = %q", got)
	}
	if !strings.Contains(rec.Body.String(), "OPENSIG") {
		t.Fatalf("thinking block must carry the signature: %s", rec.Body.String())
	}
}

func TestCovFlushTextHoldBuffer(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	if err := f.FlushTextHoldBuffer("x"); err != nil || rec.Body.Len() != 0 {
		t.Fatalf("empty buffer must be a no-op: %v %q", err, rec.Body.String())
	}
	f.textHoldBuffer = "keep this STOP and drop"
	if err := f.FlushTextHoldBuffer("STOP"); err != nil {
		t.Fatal(err)
	}
	if got := textOf(rec.Body.String()); got != "keep this " {
		t.Fatalf("got %q", got)
	}
	if f.textHoldBuffer != "" {
		t.Fatal("buffer must be cleared")
	}

	// A hold buffer that is only the stop sequence leaves nothing to emit.
	f2, rec2, _ := heldFSM(basicParams())
	f2.textHoldBuffer = "STOP"
	if err := f2.FlushTextHoldBuffer("STOP"); err != nil || textOf(rec2.Body.String()) != "" {
		t.Fatalf("got %v %q", err, textOf(rec2.Body.String()))
	}

	// Without a strip sequence everything is emitted.
	f3, rec3, _ := heldFSM(basicParams())
	f3.textHoldBuffer = "all of it"
	_ = f3.FlushTextHoldBuffer("")
	if got := textOf(rec3.Body.String()); got != "all of it" {
		t.Fatalf("got %q", got)
	}
}

func TestCovCacheCreationThreshold(t *testing.T) {
	if cacheCreationThreshold("claude-3-5-HAIKU-latest") != 2048 {
		t.Fatal("haiku floor is 2048 (case-insensitive)")
	}
	if cacheCreationThreshold("claude-opus-4-5") != 1024 || cacheCreationThreshold("") != 1024 {
		t.Fatal("non-haiku floor is 1024")
	}
}

func TestCovFlushPendingThinkingSignatureFallbackIsSafeNoop(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	f.flushPendingThinkingSignatureFallback()
	if rec.Body.Len() != 0 || f.HasFinished() {
		t.Fatal("must not emit or finish")
	}
}
