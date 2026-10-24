package main

import (
	"strings"
	"testing"
)

const sampleStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","model":"m","usage":{"input_tokens":7}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"he"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"llo"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"t","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"3}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`

func TestReadSSE_Reassembles(t *testing.T) {
	m := &msg{}
	if err := m.readSSE(strings.NewReader(sampleStream)); err != nil {
		t.Fatal(err)
	}
	if m.Text != "hello" || m.StopReason != "tool_use" || m.InputTokens != 7 || m.OutputTokens != 5 {
		t.Fatalf("bad reassembly: %+v", m)
	}
	if len(m.ToolUses) != 1 || m.ToolUses[0].Input["a"] != float64(3) || m.Raw["id"] != "msg_1" {
		t.Fatalf("tool use / id wrong: %+v raw id %v", m.ToolUses, m.Raw["id"])
	}
}

func TestReadSSE_RejectsTruncatedStream(t *testing.T) {
	cut := sampleStream[:strings.Index(sampleStream, "event: message_stop")]
	if err := (&msg{}).readSSE(strings.NewReader(cut)); err == nil {
		t.Fatal("stream without message_stop must be an error")
	}
}

func TestReadSSE_ErrorEvent(t *testing.T) {
	s := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\"}}\n\n"
	if err := (&msg{}).readSSE(strings.NewReader(s)); err == nil || !strings.Contains(err.Error(), "overloaded_error") {
		t.Fatalf("want stream error, got %v", err)
	}
}

func TestSelected(t *testing.T) {
	if !selected("tool_roundtrip", "") || !selected("tool_roundtrip", "x,roundtrip") || selected("basic", "tool") {
		t.Fatal("selected() filter wrong")
	}
}
