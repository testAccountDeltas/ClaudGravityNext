package stream

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/reqctx"
)

// heldFSM builds an FSM whose writer holds events (the production setup:
// nothing reaches the client before the first content block).
func heldFSM(p *reqctx.Params) (*FSM, *httptest.ResponseRecorder, *SafeSSEWriter) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	w.HoldEvents()
	return NewFSM(w, 1, p, &reqctx.State{}), rec, w
}

func chunk(parts, finish string) []byte {
	s := `{"candidates":[{"content":{"parts":[` + parts + `]}`
	if finish != "" {
		s += `,"finishReason":"` + finish + `"`
	}
	return []byte(s + `}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}}`)
}

func TestFinishReason_AbnormalEmptyTurn_IsRetryableErrorNotEndTurn(t *testing.T) {
	for _, fr := range []string{"MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "TOO_MANY_TOOL_CALLS",
		"MISSING_THOUGHT_SIGNATURE", "OTHER", "MALFORMED_RESPONSE", "RECITATION", "LANGUAGE", "STOP"} {
		t.Run(fr, func(t *testing.T) {
			f, rec, _ := heldFSM(basicParams())
			// thinking alone is not an answer
			err := f.ProcessGeminiChunk(chunk(`{"thought":true,"text":"hmm"}`, fr))
			var terr *UpstreamTurnError
			if !errors.As(err, &terr) {
				t.Fatalf("want *UpstreamTurnError, got %T %v", err, err)
			}
			if terr.ErrType != "api_error" && terr.ErrType != "overloaded_error" {
				t.Fatalf("bad ErrType %q", terr.ErrType)
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("nothing may reach the client while the turn can still be retried, got %s", rec.Body.String())
			}
		})
	}
}

func TestFinishReason_ToolFailureWithTextOnlyIsStillError(t *testing.T) {
	f, _, _ := heldFSM(basicParams())
	err := f.ProcessGeminiChunk(chunk(`{"text":"let me call a tool"}`, "MALFORMED_FUNCTION_CALL"))
	var terr *UpstreamTurnError
	if !errors.As(err, &terr) || terr.ErrType != "api_error" {
		t.Fatalf("MALFORMED_FUNCTION_CALL with only text must fail as api_error, got %v", err)
	}
}

func TestFinishReason_AbnormalAfterContentCommitted_EmitsSSEError(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	_ = f.ProcessGeminiChunk(chunk(`{"text":"partial answer"}`, ""))
	err := f.ProcessGeminiChunk(chunk(``, "MALFORMED_FUNCTION_CALL"))
	if err == nil {
		t.Fatal("tool failure after text must fail")
	}
	body := rec.Body.String()
	if !strings.Contains(body, "event: error") || !strings.Contains(body, `"type":"api_error"`) {
		t.Fatalf("committed content + bad finish must emit an SSE error event, got %s", body)
	}
	if strings.Contains(body, "message_stop") || strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("must not look like a clean end: %s", body)
	}
}

func TestFinishReason_Mapping(t *testing.T) {
	cases := []struct {
		fr, parts, wantStop string
	}{
		{"CONTINUATION", `{"text":"cut"}`, "max_tokens"},
		{"MAX_TOKENS", ``, "max_tokens"},
		{"RECITATION", `{"text":"quoted"}`, "end_turn"},
		{"LANGUAGE", `{"text":"hola"}`, "end_turn"},
		{"SAFETY", ``, "refusal"},
		{"PROHIBITED_CONTENT", ``, "refusal"},
		{"BLOCKLIST", ``, "refusal"},
		{"SPII", ``, "refusal"},
		{"IMAGE_SAFETY", ``, "refusal"},
		{"OTHER", `{"text":"fine"}`, "end_turn"},
	}
	for _, c := range cases {
		t.Run(c.fr, func(t *testing.T) {
			f, rec, _ := heldFSM(basicParams())
			if err := f.ProcessGeminiChunk(chunk(c.parts, c.fr)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			body := rec.Body.String()
			if got := gjson.Get(parseEventPayload(body, "message_delta"), "delta.stop_reason").String(); got != c.wantStop {
				t.Fatalf("stop_reason = %q, want %q: %s", got, c.wantStop, body)
			}
			if c.wantStop == "refusal" && (strings.Contains(body, `"type":"text"`) || !strings.Contains(body, "stop_details")) {
				t.Fatalf("refusal must have stop_details and no injected text: %s", body)
			}
		})
	}
}

func TestEmptyTurn_NoPlaceholderTextEver(t *testing.T) {
	for _, fr := range []string{"STOP", "SAFETY", "MAX_TOKENS"} {
		f, rec, _ := heldFSM(basicParams())
		_ = f.FinishStream(fr)
		body := rec.Body.String()
		if strings.Contains(body, "(empty response)") || strings.Contains(body, "not able to help") {
			t.Fatalf("%s: placeholder text injected: %s", fr, body)
		}
	}
}

func TestEOFWithoutFinishReason_IsTruncationNotEndTurn(t *testing.T) {
	// Nothing visible yet: retryable overloaded_error, nothing written.
	f, rec, _ := heldFSM(basicParams())
	err := StreamGeminiToClaude(f, strings.NewReader("data: "+string(chunk(`{"thought":true,"text":"t"}`, ""))+"\n\n"), nil)
	var terr *UpstreamTurnError
	if !errors.As(err, &terr) || terr.ErrType != "overloaded_error" {
		t.Fatalf("want truncation overloaded_error, got %v", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("held truncated turn must write nothing: %s", rec.Body.String())
	}

	// Text already streamed: SSE error event, never message_stop/end_turn.
	f2, rec2, _ := heldFSM(basicParams())
	err = StreamGeminiToClaude(f2, strings.NewReader("data: "+string(chunk(`{"text":"hello"}`, ""))+"\n\n"), nil)
	if err == nil {
		t.Fatal("expected error")
	}
	body := rec2.Body.String()
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "overloaded_error") ||
		strings.Contains(body, "message_stop") || strings.Contains(body, "end_turn") {
		t.Fatalf("truncation after content must be an SSE error: %s", body)
	}
}

func TestHold_ReleasedOnFirstTextAndHookRunsOnce(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	hooked := 0
	w.SetCommitHook(func() { hooked++ })
	w.HoldEvents()
	f := NewFSM(w, 1, basicParams(), &reqctx.State{})

	if err := f.HandleThinking("pondering", ""); err != nil {
		t.Fatal(err)
	}
	if rec.Body.Len() != 0 || hooked != 0 {
		t.Fatalf("thinking must stay held and not commit headers (hooked=%d body=%q)", hooked, rec.Body.String())
	}
	if err := f.HandleText("answer"); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if hooked != 1 {
		t.Fatalf("commit hook must run exactly once, ran %d", hooked)
	}
	iStart, iThink, iText := strings.Index(body, "message_start"), strings.Index(body, `"type":"thinking"`), strings.Index(body, `"type":"text"`)
	if !(iStart >= 0 && iStart < iThink && iThink < iText) {
		t.Fatalf("held events must flush in order before the text block: %s", body)
	}
}

func TestHold_PingBypassesHoldAndCommits(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	hooked := 0
	w.SetCommitHook(func() { hooked++ })
	w.HoldEvents()
	_ = w.EmitEvent("message_start", `{}`)
	w.Commit()
	_ = w.EmitEvent("ping", `{"type":"ping"}`)
	if hooked != 1 || !strings.Contains(rec.Body.String(), "event: ping") || strings.Contains(rec.Body.String(), "message_start") {
		t.Fatalf("ping must go out immediately, held events must not: hooked=%d %q", hooked, rec.Body.String())
	}
	if !w.HeadersSent() {
		t.Fatal("headers must count as sent after a ping")
	}
}

func TestInBandError_BeforeContent_IsTypedRetryableError(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	err := f.ProcessGeminiChunk([]byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota"}}`))
	var terr *UpstreamTurnError
	if !errors.As(err, &terr) || terr.Status != 429 || !terr.InBand || terr.ErrType != "rate_limit_error" {
		t.Fatalf("want in-band 429 typed error, got %#v / %v", terr, err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("nothing may be written: %s", rec.Body.String())
	}
}

func TestStopSequence_EchoedInTailIsDetectedAndCut(t *testing.T) {
	p := basicParams()
	p.StopSequences = []string{"END", "STOP_HERE"}
	f, rec, _ := heldFSM(p)
	if err := f.ProcessGeminiChunk(chunk(`{"text":"first part. then STOP_HERE"}`, "STOP")); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	delta := parseEventPayload(body, "message_delta")
	if gjson.Get(delta, "delta.stop_reason").String() != "stop_sequence" || gjson.Get(delta, "delta.stop_sequence").String() != "STOP_HERE" {
		t.Fatalf("expected stop_sequence STOP_HERE: %s", delta)
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "text_delta") && strings.Contains(line, "STOP_HERE") {
			t.Fatalf("matched sequence leaked into text: %s", line)
		}
	}
}

func TestStopSequence_StrippedByGemini_StaysEndTurn(t *testing.T) {
	// Documented uncertainty: Gemini strips the matched sequence and sends a
	// plain STOP, indistinguishable from a natural end.
	p := basicParams()
	p.StopSequences = []string{"END"}
	f, rec, _ := heldFSM(p)
	if err := f.ProcessGeminiChunk(chunk(`{"text":"all done"}`, "STOP")); err != nil {
		t.Fatal(err)
	}
	if got := gjson.Get(parseEventPayload(rec.Body.String(), "message_delta"), "delta.stop_reason").String(); got != "end_turn" {
		t.Fatalf("want end_turn, got %s", got)
	}
}

func TestMessageDeltaUsage_CarriesRealInputAndCache(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	um := `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1000,"cachedContentTokenCount":600,"candidatesTokenCount":7}}`
	if err := f.ProcessGeminiChunk([]byte(um)); err != nil {
		t.Fatal(err)
	}
	u := gjson.Get(parseEventPayload(rec.Body.String(), "message_delta"), "usage")
	if u.Get("input_tokens").Int() != 400 || u.Get("cache_read_input_tokens").Int() != 600 ||
		!u.Get("cache_creation_input_tokens").Exists() || u.Get("output_tokens").Int() != 7 {
		t.Fatalf("message_delta.usage = %s", u.Raw)
	}
	if st := f.Stats(); st.FinishReason != "STOP" || st.StopReason != "end_turn" {
		t.Fatalf("stats must expose finish/stop reason, got %+v", st)
	}
}

func TestPromptFeedbackBlock_RefusalWithoutInjectedText(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	if err := f.ProcessGeminiChunk([]byte(`{"promptFeedback":{"blockReason":"SAFETY"}}`)); err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	if strings.Contains(body, `"type":"text"`) || !strings.Contains(body, `"stop_reason":"refusal"`) ||
		!strings.Contains(body, "promptFeedback.blockReason=SAFETY") {
		t.Fatalf("unexpected block output: %s", body)
	}
}
