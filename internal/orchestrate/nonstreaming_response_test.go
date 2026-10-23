package orchestrate

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"claudgravity/internal/reqctx"
)

func TestApplyPrefillToResponse_PrependsToFirstTextBlock(t *testing.T) {
	resp := map[string]interface{}{"content": []map[string]interface{}{
		{"type": "text", "text": "answer"},
	}}
	applyPrefillToResponse(resp, "{\"")
	blocks := resp["content"].([]map[string]interface{})
	if blocks[0]["text"] != "{\"answer" {
		t.Fatalf("expected prefill prepended, got %q", blocks[0]["text"])
	}
}

func TestApplyPrefillToResponse_SkipsThinkingBlockFindsTextBlock(t *testing.T) {
	// A Round 15 v1 audit caught the original bug: this only ever looked
	// at content[0], which is the "thinking" block whenever extended
	// thinking is on — the prefill silently never got re-attached.
	resp := map[string]interface{}{"content": []map[string]interface{}{
		{"type": "thinking", "thinking": "reasoning"},
		{"type": "text", "text": "answer"},
	}}
	applyPrefillToResponse(resp, "PREFIX-")
	blocks := resp["content"].([]map[string]interface{})
	if blocks[1]["text"] != "PREFIX-answer" {
		t.Fatalf("expected the prefill to land on the first TEXT block, not index 0, got %q", blocks[1]["text"])
	}
}

func TestApplyPrefillToResponse_NoOpWhenAlreadyPrefixed(t *testing.T) {
	resp := map[string]interface{}{"content": []map[string]interface{}{
		{"type": "text", "text": "PREFIX-already there"},
	}}
	applyPrefillToResponse(resp, "PREFIX-")
	blocks := resp["content"].([]map[string]interface{})
	if blocks[0]["text"] != "PREFIX-already there" {
		t.Fatalf("expected no double-prefixing when Gemini already echoed the prefill, got %q", blocks[0]["text"])
	}
}

func TestApplyPrefillToResponse_EmptyPrefillIsNoOp(t *testing.T) {
	resp := map[string]interface{}{"content": []map[string]interface{}{
		{"type": "text", "text": "answer"},
	}}
	applyPrefillToResponse(resp, "")
	blocks := resp["content"].([]map[string]interface{})
	if blocks[0]["text"] != "answer" {
		t.Fatal("expected no change with an empty prefill")
	}
}

func TestWriteBlockedResponse_ProducesRefusalShapedMessageNotErrorBody(t *testing.T) {
	p := &reqctx.Params{RID: 1, RawClaudeBody: []byte(`{"model":"claude-opus-5-5","messages":[]}`)}
	st := &reqctx.State{ResolvedModelName: "gemini-3.8-flash-tiered"}
	w := httptest.NewRecorder()

	ok, status, err := writeBlockedResponse(p, st, "acc@example.com", p.RawClaudeBody, "SAFETY", w)
	if !ok || status != 200 || err != nil {
		t.Fatalf("expected a committed 200 response, got ok=%v status=%d err=%v", ok, status, err)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("expected valid JSON body: %v", err)
	}
	if body["type"] != "message" {
		t.Fatalf(`expected a real message object (type:"message"), not an error envelope — official Anthropic SDKs reject an {"type":"error",...} body on HTTP 200 outright; got %v`, body["type"])
	}
	if body["stop_reason"] != "refusal" {
		t.Fatalf("expected stop_reason refusal, got %v", body["stop_reason"])
	}
}
