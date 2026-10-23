// Tests for the new claude protocol domain — not a characterization test
// against a specific v1 function (there's no single v1 equivalent), but
// this is exactly the foundational new code every future translate/
// stream/orchestrate package will trust, so it needs its own direct
// correctness tests: round-trip fidelity and the union-type decoders'
// explicit-false behavior on a type mismatch.
package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessage_RoundTripsUnknownBlockTypeByteForByte(t *testing.T) {
	raw := []byte(`{"role":"assistant","content":[{"type":"some_future_block_type","weird_field":[1,2,3]}]}`)
	var m Message
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var roundTrip, original interface{}
	_ = json.Unmarshal(out, &roundTrip)
	_ = json.Unmarshal(raw, &original)
	outJSON, _ := json.Marshal(roundTrip)
	origJSON, _ := json.Marshal(original)
	if string(outJSON) != string(origJSON) {
		t.Fatalf("an unknown block type must round-trip byte-for-byte (modulo key order), got %s want %s", outJSON, origJSON)
	}
}

func TestBlock_AsTextReturnsFalseOnWrongType(t *testing.T) {
	raw := []byte(`{"type":"tool_use","id":"t1","name":"Read","input":{}}`)
	var b Block
	_ = json.Unmarshal(raw, &b)
	if _, ok := b.AsText(); ok {
		t.Fatal("AsText must return ok=false for a non-text block, not panic or silently misdecode")
	}
	tu, ok := b.AsToolUse()
	if !ok || tu.Name != "Read" {
		t.Fatalf("expected AsToolUse to decode correctly, got %+v ok=%v", tu, ok)
	}
}

func TestToolResultBlock_ContentTextVsBlocks(t *testing.T) {
	strContent := []byte(`{"type":"tool_result","tool_use_id":"t1","content":"plain text"}`)
	var b1 Block
	_ = json.Unmarshal(strContent, &b1)
	tr1, _ := b1.AsToolResult()
	text, ok := tr1.ContentText()
	if !ok || text != "plain text" {
		t.Fatalf("expected string content to decode, got %q ok=%v", text, ok)
	}
	if _, ok := tr1.ContentBlocks(); ok {
		t.Fatal("ContentBlocks must return ok=false when Content is actually a string")
	}

	arrContent := []byte(`{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"hi"}]}`)
	var b2 Block
	_ = json.Unmarshal(arrContent, &b2)
	tr2, _ := b2.AsToolResult()
	blocks, ok := tr2.ContentBlocks()
	if !ok || len(blocks) != 1 {
		t.Fatalf("expected array content to decode as 1 block, got %v ok=%v", blocks, ok)
	}
	if _, ok := tr2.ContentText(); ok {
		t.Fatal("ContentText must return ok=false when Content is actually an array")
	}
}

func TestMessage_ContentAsBareString(t *testing.T) {
	raw := []byte(`{"role":"user","content":"hello"}`)
	var m Message
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !m.ContentWasBareString {
		t.Fatal("expected ContentWasBareString=true for a bare-string content message")
	}
	text, ok := m.Content[0].AsText()
	if !ok || text.Text != "hello" {
		t.Fatalf("got content=%+v ok=%v", text, ok)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m2 Message
	if err := json.Unmarshal(out, &m2); err != nil {
		t.Fatalf("re-unmarshal after marshal: %v", err)
	}
	if m2.ContentWasBareString {
		t.Fatal("marshal must always emit the array form, even if the original was a bare string")
	}
	text2, ok := m2.Content[0].AsText()
	if !ok || text2.Text != "hello" {
		t.Fatalf("got content=%+v ok=%v after round trip", text2, ok)
	}
}

func TestToolResultBlock_CacheControlSurvivesDecodeReencode(t *testing.T) {
	raw := []byte(`{"type":"tool_result","tool_use_id":"t1","content":"hi","cache_control":{"type":"ephemeral"}}`)
	var b Block
	_ = json.Unmarshal(raw, &b)
	tr, ok := b.AsToolResult()
	if !ok {
		t.Fatal("expected AsToolResult to decode")
	}
	tr.Content, _ = json.Marshal("hi (mutated)")
	out := NewBlock(tr)
	if string(out.Raw) == "" {
		t.Fatal("expected non-empty re-encoded block")
	}
	var decoded map[string]interface{}
	_ = json.Unmarshal(out.Raw, &decoded)
	if _, ok := decoded["cache_control"]; !ok {
		t.Fatalf("cache_control must survive a decode-mutate-reencode cycle, got %s", out.Raw)
	}
}

func TestRequest_SystemText_BareString(t *testing.T) {
	req := Request{System: SystemPrompt(`"you are a helpful assistant"`)}
	if got := req.SystemText(); got != "you are a helpful assistant" {
		t.Fatalf("got %q", got)
	}
}

func TestRequest_SystemText_ArrayOfBlocks(t *testing.T) {
	req := Request{System: SystemPrompt(`[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]`)}
	got := req.SystemText()
	if got != "line one\nline two\n" {
		t.Fatalf("got %q", got)
	}
}

// TestRequest_UnmarshalJSON_SystemArrayOfBlocks is a direct regression
// test for a real production bug: the two tests above construct
// SystemPrompt directly from raw bytes, never exercising
// json.Unmarshal on a Request at all, so they never caught that
// SystemPrompt (a type DEFINITION over json.RawMessage, not an alias)
// had no UnmarshalJSON of its own and silently fell back to default
// []byte decoding -- which errors on an array of objects with
// "cannot unmarshal object into Request.system.0 of type uint8".
// This goes through the real json.Unmarshal(Request) path Claude Code's
// actual traffic takes.
func TestRequest_UnmarshalJSON_SystemArrayOfBlocks(t *testing.T) {
	raw := []byte(`{"model":"gemini-2.5-flash","max_tokens":16,"system":[{"type":"text","text":"line one"},{"type":"text","text":"line two"}],"messages":[]}`)
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal Request with array-of-blocks system: %v", err)
	}
	if got := req.SystemText(); got != "line one\nline two\n" {
		t.Fatalf("got %q", got)
	}
}

func TestRequest_UnmarshalJSON_SystemBareString(t *testing.T) {
	raw := []byte(`{"model":"gemini-2.5-flash","max_tokens":16,"system":"you are a helpful assistant","messages":[]}`)
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal Request with bare-string system: %v", err)
	}
	if got := req.SystemText(); got != "you are a helpful assistant" {
		t.Fatalf("got %q", got)
	}
	reenc, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	var decoded map[string]interface{}
	_ = json.Unmarshal(reenc, &decoded)
	if decoded["system"] != "you are a helpful assistant" {
		t.Fatalf("system must round-trip through MarshalJSON too, got %v", decoded["system"])
	}
}

// GAP-T1/T2/T3: temperature/top_p/top_k/disable_parallel_tool_use/
// output_format had no struct field at all, so translate.go's
// json.Unmarshal(claudeJSON, &req) -> ... -> json.Marshal(req) round-trip
// silently dropped them before internal/translate's gjson-based readers
// ever got a chance to see them, regardless of what those readers
// themselves did right.
func TestRequest_SamplingAndOutputFormatFields_SurviveRoundTrip(t *testing.T) {
	raw := []byte(`{
		"model": "claude-opus-4-5",
		"messages": [{"role": "user", "content": "hi"}],
		"max_tokens": 16,
		"temperature": 0.2,
		"top_p": 0.9,
		"top_k": 40,
		"disable_parallel_tool_use": true,
		"output_format": {"type": "json_schema", "schema": {"type": "object"}}
	}`)
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Temperature == nil || *req.Temperature != 0.2 {
		t.Fatalf("expected Temperature=0.2, got %+v", req.Temperature)
	}
	if req.TopP == nil || *req.TopP != 0.9 {
		t.Fatalf("expected TopP=0.9, got %+v", req.TopP)
	}
	if req.TopK == nil || *req.TopK != 40 {
		t.Fatalf("expected TopK=40, got %+v", req.TopK)
	}
	if req.DisableParallelToolUse == nil || !*req.DisableParallelToolUse {
		t.Fatalf("expected DisableParallelToolUse=true, got %+v", req.DisableParallelToolUse)
	}
	if len(req.OutputFormat) == 0 {
		t.Fatal("expected OutputFormat to survive decode")
	}

	reencoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(reencoded, &decoded); err != nil {
		t.Fatalf("unmarshal re-encoded: %v", err)
	}
	for _, field := range []string{"temperature", "top_p", "top_k", "disable_parallel_tool_use", "output_format"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("expected %q to survive the full struct round-trip, got %v", field, decoded)
		}
	}
}

func TestRequest_FullRoundTrip(t *testing.T) {
	raw := []byte(`{
		"model": "claude-opus-4-5",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello"}]}
		],
		"max_tokens": 1024,
		"stream": true,
		"tools": [{"name": "Read", "input_schema": {"type": "object"}}]
	}`)
	var req Request
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Model != "claude-opus-4-5" || req.MaxTokens != 1024 || !req.Stream {
		t.Fatalf("got %+v", req)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].Content) != 1 {
		t.Fatalf("got messages=%+v", req.Messages)
	}
	text, ok := req.Messages[0].Content[0].AsText()
	if !ok || text.Text != "hello" {
		t.Fatalf("got text=%+v ok=%v", text, ok)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "Read" {
		t.Fatalf("got tools=%+v", req.Tools)
	}
	}

func TestStopReason_PauseTurn_Serialization(t *testing.T) {
		reasons := []struct {
		reason StopReason
		want   string
		}{
		{StopEndTurn, `"end_turn"`},
		{StopMaxTokens, `"max_tokens"`},
		{StopToolUse, `"tool_use"`},
		{StopStopSequence, `"stop_sequence"`},
		{StopRefusal, `"refusal"`},
		{StopPauseTurn, `"pause_turn"`},
		}

		for _, tc := range reasons {
		b, err := json.Marshal(tc.reason)
		if err != nil {
		t.Fatalf("marshal %s: %v", tc.reason, err)
		}
		if string(b) != tc.want {
		t.Errorf("got %s, want %s", string(b), tc.want)
		}
		var decoded StopReason
		if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal %s: %v", tc.reason, err)
		}
		if decoded != tc.reason {
		t.Errorf("decoded %s, want %s", decoded, tc.reason)
		}
		}
		}

		func TestMessageDeltaEvent_UsageContract_NoInputTokens(t *testing.T) {
		ev := MessageDeltaEvent{
		Type: "message_delta",
		Delta: MessageDelta{
		StopReason: StopPauseTurn,
		},
		Usage: MessageDeltaUsage{
		OutputTokens: 42,
		OutputTokensDetails: &OutputTokensDetails{
		ThinkingTokens: 15,
		},
		},
		}
		b, err := json.Marshal(ev)
		if err != nil {
		t.Fatalf("marshal: %v", err)
		}
		str := string(b)
		if !strings.Contains(str, `"stop_reason":"pause_turn"`) {
		t.Errorf("expected stop_reason:pause_turn, got: %s", str)
		}
		if !strings.Contains(str, `"output_tokens":42`) {
		t.Errorf("expected output_tokens:42, got: %s", str)
		}
		if !strings.Contains(str, `"thinking_tokens":15`) {
		t.Errorf("expected thinking_tokens:15, got: %s", str)
		}
		if strings.Contains(str, "input_tokens") {
		t.Errorf("message_delta must strictly NOT contain input_tokens, got: %s", str)
		}
		}

