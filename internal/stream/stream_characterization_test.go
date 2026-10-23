// Characterization tests for internal/stream against v1's sse.go, plus
// direct tests of the new SafeSSEWriter (no v1 equivalent — built
// against the real GAP-D1-01/GAP-D1-02 incidents).
package stream

import (
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"claudgravity/internal/reqctx"
	"claudgravity/internal/toolsig"
)

func newTestFSM(t *testing.T, p *reqctx.Params, st *reqctx.State) (*FSM, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, st)
	return f, rec
}

func basicParams() *reqctx.Params {
	return &reqctx.Params{ModelName: "claude-opus-4-5"}
}

// --- SafeSSEWriter / GAP-D1-01 ---

func TestSafeSSEWriter_TerminateWithError_BeforeHeaders_ReportsNotSentAsSSE(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	sentAsSSE := w.TerminateWithError("api_error", "boom")
	if sentAsSSE {
		t.Fatal("expected sentAsSSE=false when no event has been emitted yet")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected no bytes written, got %q", rec.Body.String())
	}
}

func TestSafeSSEWriter_TerminateWithError_AfterHeaders_EmitsSSEError(t *testing.T) {
	// This is the exact GAP-D1-01 scenario: a response that already
	// committed message_start (headers + at least one SSE event) must
	// never ALSO get a fresh HTTP status written — only a trailing
	// event:error frame is safe.
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	_ = w.EmitEvent("message_start", `{"type":"message_start"}`)
	sentAsSSE := w.TerminateWithError("api_error", "boom")
	if !sentAsSSE {
		t.Fatal("expected sentAsSSE=true once headers have gone out")
	}
	if !strings.Contains(rec.Body.String(), `"type":"error"`) {
		t.Fatalf("expected an event:error frame in the body, got %q", rec.Body.String())
	}
}

func TestSafeSSEWriter_EmitEvent_NoOpAfterFinish(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	w.Finish()
	_ = w.EmitEvent("message_stop", `{"type":"message_stop"}`)
	if rec.Body.Len() != 0 {
		t.Fatalf("expected no bytes written after Finish, got %q", rec.Body.String())
	}
}

func TestSafeSSEWriter_HeadersSent_TracksFirstEmit(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	if w.HeadersSent() {
		t.Fatal("expected HeadersSent=false before any event")
	}
	_ = w.EmitEvent("ping", "{}")
	if !w.HeadersSent() {
		t.Fatal("expected HeadersSent=true after an event")
	}
}

// GAP-S1: SendKeepalivePing puts real bytes on the wire but used to never
// call commitHeadersLocked() — a later TerminateWithError would then see
// headersSent still false and try to write a fresh HTTP status on top of
// a connection that already has ping bytes on it, panicking with
// "superfluous response.WriteHeader".
func TestSafeSSEWriter_SendKeepalivePing_CommitsHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	// Bypass the 4-second throttle directly (white-box, same package) —
	// the real bug was in commitHeadersLocked()'s absence, not the timer.
	w.lastEventTime = w.lastEventTime.Add(-10 * time.Second)
	if err := w.SendKeepalivePing(); err != nil {
		t.Fatalf("SendKeepalivePing: %v", err)
	}
	if !w.HeadersSent() {
		t.Fatal("expected HeadersSent=true after a ping actually wrote bytes")
	}
}

// GAP-S8: FinishWithError (the mid-stream connection-drop fallback) must
// close whatever content block was open before emitting event:error — a
// missing content_block_stop for an already-started block violates the
// SSE state machine a strict client enforces.
func TestFSM_FinishWithError_ClosesOpenContentBlock(t *testing.T) {
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	if err := f.HandleText("partial answer"); err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	f.FinishWithError(errors.New("connection reset"))
	body := rec.Body.String()
	if !strings.Contains(body, "content_block_stop") {
		t.Fatalf("expected content_block_stop before the error event, got %s", body)
	}
	if !strings.Contains(body, `"type":"error"`) {
		t.Fatalf("expected an error event, got %s", body)
	}
}

// --- FSM / GAP-D1-02 (prefill) ---

func TestFSM_ApplyPrefill_StreamingPathNowAppliesIt(t *testing.T) {
	// The actual GAP-D1-02 regression: the streaming path previously had
	// no prefill application at all. HandleText must prepend it on the
	// first real text delta.
	p := basicParams()
	p.PrefillText = "{"
	f, rec := newTestFSM(t, p, &reqctx.State{})
	_ = f.HandleText("\"key\": 1}")
	body := rec.Body.String()
	if !strings.Contains(body, `{\"key\": 1}`) {
		t.Fatalf("expected prefill prepended to first text delta, got %s", body)
	}
}

func TestFSM_ApplyPrefill_NotDoubleAppliedOnSecondDelta(t *testing.T) {
	p := basicParams()
	p.PrefillText = "{"
	f, rec := newTestFSM(t, p, &reqctx.State{})
	_ = f.HandleText("a")
	_ = f.HandleText("b")
	body := rec.Body.String()
	if strings.Contains(body, `"text":"{a"`) && strings.Contains(body, `"text":"{b"`) {
		t.Fatalf("prefill must only apply once, got %s", body)
	}
}

// GAP-PRF-02: ApplyPrefill was only ever called from HandleText — if the
// model's very first action in the turn is a tool call, HandleText never
// runs and the prefill was silently dropped for the whole turn.
func TestFSM_ApplyPrefill_EmittedBeforeImmediateToolCall(t *testing.T) {
	p := basicParams()
	p.PrefillText = "Let me check: "
	f, rec := newTestFSM(t, p, &reqctx.State{})
	if err := f.HandleFunctionCall("Bash", `{"command":"ls"}`, ""); err != nil {
		t.Fatalf("HandleFunctionCall: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Let me check:") {
		t.Fatalf("expected the prefill to be emitted as a leading text block before the tool_use, got %s", body)
	}
	if !strings.Contains(body, `"type":"tool_use"`) {
		t.Fatalf("expected the tool_use block to still be emitted, got %s", body)
	}
}

// GAP-SSE-01: FinishStream never called writer.Finish(), and
// ProcessGeminiChunk had no guard against processing a chunk that
// arrives AFTER FinishStream already ran (several Gemini SSE "data:"
// lines can land in the same read() buffer) — either gap alone let a
// trailing chunk write more SSE frames after message_stop.
func TestFSM_FinishStream_WriterFinished_NoMoreEventsAfterMessageStop(t *testing.T) {
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	if err := f.HandleText("hello"); err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}
	lenBefore := rec.Body.Len()
	if !f.writer.finished {
		t.Fatal("expected writer.Finish() to have been called by FinishStream")
	}
	// A trailing chunk from the same upstream read buffer must be a
	// complete no-op: no FSM state mutation, no bytes written.
	if err := f.ProcessGeminiChunk([]byte(`{"candidates":[{"content":{"parts":[{"text":"late"}]}}]}`)); err != nil {
		t.Fatalf("ProcessGeminiChunk after finish: %v", err)
	}
	if rec.Body.Len() != lenBefore {
		t.Fatalf("expected no additional bytes written after FinishStream, body grew from %d to %d: %s", lenBefore, rec.Body.Len(), rec.Body.String())
	}
}

// --- FSM behaviors ported from sse.go ---

func TestFSM_SecondThinkingBlock_Dropped(t *testing.T) {
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	_ = f.HandleThinking("first thought", "sig1")
	_ = f.closeCurrentBlock()
	_ = f.HandleThinking("second thought — should be dropped", "")
	body := rec.Body.String()
	if strings.Contains(body, "second thought") {
		t.Fatalf("a second thinking block after the first closed must be dropped, got %s", body)
	}
}

func TestFSM_TextAfterToolUse_Dropped(t *testing.T) {
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	_ = f.HandleFunctionCall("Read", `{"file_path":"a.go"}`, "")
	_ = f.HandleText("trailing text should be dropped")
	body := rec.Body.String()
	if strings.Contains(body, "trailing text") {
		t.Fatalf("text after a tool_use block must be dropped, got %s", body)
	}
}

func TestFSM_DisableParallelToolUse_DropsSecondCall(t *testing.T) {
	p := basicParams()
	p.DisableParallelToolUse = true
	f, rec := newTestFSM(t, p, &reqctx.State{})
	_ = f.HandleFunctionCall("Read", `{}`, "")
	_ = f.HandleFunctionCall("Write", `{}`, "")
	body := rec.Body.String()
	if strings.Count(body, `"tool_use"`) != 1 {
		t.Fatalf("expected exactly one tool_use block with disableParallelToolUse, got %s", body)
	}
}

func TestFSM_FinishStream_MaxTokensWithZeroContent_ReportsMaxTokens(t *testing.T) {
	// The exact bug an audit caught: deciding stopReason and emitting the
	// empty-content fallback must not be entangled — a MAX_TOKENS cutoff
	// with zero content blocks must still report "max_tokens", not
	// "end_turn".
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	if err := f.FinishStream("MAX_TOKENS"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"stop_reason":"max_tokens"`) {
		t.Fatalf("expected stop_reason max_tokens, got %s", body)
	}
}

func TestFSM_FinishStream_ToolUse_ReportsToolUseStopReason(t *testing.T) {
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	_ = f.HandleFunctionCall("Read", `{}`, "")
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Fatalf("expected stop_reason tool_use, got %s", body)
	}
}

func TestFSM_FinishStream_RecoversStopSequence(t *testing.T) {
	p := basicParams()
	p.StopSequences = []string{"STOP_HERE"}
	f, rec := newTestFSM(t, p, &reqctx.State{})
	_ = f.HandleText("some text STOP_HERE")
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"stop_reason":"stop_sequence"`) {
		t.Fatalf("expected stop_reason stop_sequence, got %s", body)
	}
}

func TestFSM_FinishStream_IdempotentOnDoubleCall(t *testing.T) {
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("first FinishStream: %v", err)
	}
	firstLen := rec.Body.Len()
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("second FinishStream: %v", err)
	}
	if rec.Body.Len() != firstLen {
		t.Fatalf("a second FinishStream call must be a no-op, body grew from %d to %d", firstLen, rec.Body.Len())
	}
}

// --- ProcessGeminiChunk ---

func TestProcessGeminiChunk_InBandError_MapsResourceExhaustedToRateLimit(t *testing.T) {
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	chunk := []byte(`{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`)
	err := f.ProcessGeminiChunk(chunk)
	if err == nil {
		t.Fatal("expected an error to be returned for an in-band error chunk")
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"type":"rate_limit_error"`) {
		t.Fatalf("expected rate_limit_error type, got %s", body)
	}
}

func TestProcessGeminiChunk_PromptFeedbackBlocked_EmitsSafetyMessage(t *testing.T) {
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	chunk := []byte(`{"promptFeedback":{"blockReason":"SAFETY"}}`)
	if err := f.ProcessGeminiChunk(chunk); err != nil {
		t.Fatalf("ProcessGeminiChunk: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "flagged by upstream safety filters") {
		t.Fatalf("expected safety-block message, got %s", body)
	}
	if !strings.Contains(body, `"stop_reason":"refusal"`) {
		t.Fatalf("expected stop_reason refusal, got %s", body)
	}
}

func TestProcessGeminiChunk_FunctionCallArgsNull_BecomesEmptyObject(t *testing.T) {
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	chunk := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"Read","args":null}}]}}]}`)
	if err := f.ProcessGeminiChunk(chunk); err != nil {
		t.Fatalf("ProcessGeminiChunk: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"partial_json":"{}"`) {
		t.Fatalf("expected a literal null args to become {}, got %s", body)
	}
}

func TestProcessGeminiChunk_ReverseToolNameMap_RestoresOriginalName(t *testing.T) {
	p := basicParams()
	p.ReverseToolNameMap = map[string]string{"mcp__server__search": "mcp__server-search"}
	f, rec := newTestFSM(t, p, &reqctx.State{})
	chunk := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"mcp__server__search","args":{}}}]}}]}`)
	if err := f.ProcessGeminiChunk(chunk); err != nil {
		t.Fatalf("ProcessGeminiChunk: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"name":"mcp__server-search"`) {
		t.Fatalf("expected original tool name restored, got %s", body)
	}
}

func TestProcessGeminiChunk_UsageMetadata_ThinkingTokensAddedToOutput(t *testing.T) {
	f, _ := newTestFSM(t, basicParams(), &reqctx.State{})
	chunk := []byte(`{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":0,"thoughtsTokenCount":50}}`)
	if err := f.ProcessGeminiChunk(chunk); err != nil {
		t.Fatalf("ProcessGeminiChunk: %v", err)
	}
	if f.outputTokens != 50 {
		t.Fatalf("expected outputTokens=50 (pure-thinking turn), got %d", f.outputTokens)
	}
}

func TestProcessGeminiChunk_GroundingSources_AppendedAsFooterOnFinish(t *testing.T) {
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	chunk := []byte(`{"candidates":[{"content":{"parts":[{"text":"answer"}]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://example.com","title":"Example"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":10}}`)
	if err := f.ProcessGeminiChunk(chunk); err != nil {
		t.Fatalf("ProcessGeminiChunk: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Google Search Sources") || !strings.Contains(body, "https://example.com") {
		t.Fatalf("expected a grounding sources footer, got %s", body)
	}
}

func TestProcessGeminiChunk_ResponseEnvelopeUnwrapped(t *testing.T) {
	f, _ := newTestFSM(t, basicParams(), &reqctx.State{})
	chunk := []byte(`{"response":{"usageMetadata":{"promptTokenCount":42}}}`)
	if err := f.ProcessGeminiChunk(chunk); err != nil {
		t.Fatalf("ProcessGeminiChunk: %v", err)
	}
	if f.inputTokens != 42 {
		t.Fatalf("expected the response envelope to be unwrapped, got inputTokens=%d", f.inputTokens)
	}
}

// --- toolcalls.go ---

func TestNormalizeToolCallArgsToObject_BareStringMapsToKnownPrimaryParam(t *testing.T) {
	out := normalizeToolCallArgsToObject("bash", `"git status"`)
	if out != `{"command":"git status"}` {
		t.Fatalf("got %s", out)
	}
}

func TestNormalizeToolCallArgsToObject_DoubleEncoded_Decodes(t *testing.T) {
	out := normalizeToolCallArgsToObject("Read", `"{\"file_path\":\"a.go\"}"`)
	if !strings.Contains(out, `"file_path":"a.go"`) {
		t.Fatalf("got %s", out)
	}
}

func TestNextToolCallID_NeverExceeds64Chars(t *testing.T) {
	id := nextToolCallID(strings.Repeat("mcp__very_long_tool_name_", 5))
	if len(id) > 64 {
		t.Fatalf("expected id <= 64 chars, got %d: %s", len(id), id)
	}
}

// GAP-P0-04/P0-06: an in-band Gemini error (code 200 outer HTTP, an
// "error" field inside the chunk itself) with status DEADLINE_EXCEEDED
// must map to overloaded_error (so Claude Code's own SDK retries instead
// of treating it as fatal), and the FSM must close whatever content
// block was open BEFORE emitting the error event — a client parser
// enforcing the SSE state machine strictly rejects an error event that
// arrives with an unclosed content_block_start.
func TestFSM_HandleInBandError_ClosesBlockAndMapsDeadlineExceeded(t *testing.T) {
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	if err := f.HandleText("partial answer"); err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	chunk := []byte(`{"error":{"status":"DEADLINE_EXCEEDED","code":4,"message":"upstream timed out"}}`)
	if err := f.ProcessGeminiChunk(chunk); err == nil {
		t.Fatal("expected ProcessGeminiChunk to return an error for an in-band error chunk")
	}
	body := rec.Body.String()
	stopIdx := strings.Index(body, "content_block_stop")
	errIdx := strings.Index(body, `"type":"error"`)
	if stopIdx == -1 || errIdx == -1 {
		t.Fatalf("expected both content_block_stop and an error event, got %s", body)
	}
	if stopIdx > errIdx {
		t.Fatalf("expected content_block_stop BEFORE the error event, got %s", body)
	}
	if !strings.Contains(body, `"overloaded_error"`) {
		t.Fatalf("expected DEADLINE_EXCEEDED to map to overloaded_error, got %s", body)
	}
}

// GAP-P2-03: the non-streaming response path always includes
// "stop_details" (null outside a refusal) — the streaming message_delta
// used to omit the key entirely on a normal finish, an inconsistency a
// strict typed SDK could trip over.
func TestFSM_FinishStream_AlwaysIncludesStopDetailsKey(t *testing.T) {
	f, rec := newTestFSM(t, basicParams(), &reqctx.State{})
	if err := f.HandleText("hello"); err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"stop_details":null`) {
		t.Fatalf(`expected "stop_details":null present on a normal finish, got %s`, body)
	}
}

// GAP-D1-04: if Gemini echoes the prefill back split across chunks —
// this first chunk being a strict PREFIX of prefillText (e.g.
// prefillText=="```json\n{" and this chunk is only "```json") — the
// prefill must not be duplicated on top of that in-progress echo.
func TestPrependPrefillOnce_PartialEchoChunk_NotDuplicated(t *testing.T) {
	emitted := false
	got := PrependPrefillOnce("```json\n{", "```json", &emitted)
	if got != "```json" {
		t.Fatalf("expected the partial echo left untouched, got %q", got)
	}
}

// Companion case: a genuinely NEW, non-echoing first chunk still gets
// the real prefill prepended exactly once.
func TestPrependPrefillOnce_NonEchoChunk_PrefillPrepended(t *testing.T) {
	emitted := false
	got := PrependPrefillOnce("```json\n{", "unrelated text", &emitted)
	if got != "```json\n{unrelated text" {
		t.Fatalf("expected prefill prepended, got %q", got)
	}
}

// Regression for the bug this same fix introduced and the existing
// TestFSM_ApplyPrefill_EmittedBeforeImmediateToolCall test caught:
// HandleFunctionCall calls ApplyPrefill("") deliberately to still emit
// the prefill as its own leading text block when the model's first
// action is a tool call — strings.HasPrefix(prefillText, "") is always
// true, so the empty-chunk case must not be treated as an echo.
func TestPrependPrefillOnce_EmptyChunk_StillGetsRealPrefill(t *testing.T) {
	emitted := false
	got := PrependPrefillOnce("```json\n{", "", &emitted)
	if got != "```json\n{" {
		t.Fatalf("expected the prefill itself for an empty chunk, got %q", got)
	}
}

func TestFSM_FinishWithError_PreventsSubsequentPings(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	p := basicParams()
	f := NewFSM(w, 1, p, &reqctx.State{})
	_ = f.HandleText("initial")
	f.FinishWithError(errors.New("connection failed"))

	bodyBefore := rec.Body.String()
	if !strings.Contains(bodyBefore, `"overloaded_error"`) {
		t.Fatalf("expected overloaded_error in body, got %s", bodyBefore)
	}
	// Try sending a ping after finish
	_ = w.SendKeepalivePing()
	_ = w.EmitEvent("ping", `{"type":"ping"}`)
	bodyAfter := rec.Body.String()
	if bodyBefore != bodyAfter {
		t.Fatalf("expected writer to be finished and ignore subsequent writes, got %s", bodyAfter)
	}
}

func TestFSM_CacheMiss_ResetsSessionFraction(t *testing.T) {
	sessionID := "sess_cache_test"
	SetSessionCacheFraction(sessionID, 0.85)
	p := basicParams()
	p.SessionID = sessionID
	p.HasPriorTurns = true
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	st := &reqctx.State{EstInputTokens: 2000}
	f := NewFSM(w, 1, p, st)
	f.inputTokens = 2000
	f.cacheTokens = 0
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}
	frac, ok := getSessionCacheFraction(sessionID)
	if !ok || frac != 0.0 {
		t.Fatalf("expected cache fraction to be reset to 0.0 on cache miss, got %v (ok=%v)", frac, ok)
	}
}

func TestFSM_FinishStream_RefusalClearsPrefill(t *testing.T) {
	p := basicParams()
	p.PrefillText = "```json\n{"
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})
	if err := f.FinishStream("SAFETY"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}
	body := rec.Body.String()
	if strings.Contains(body, "```json") {
		t.Fatalf("expected prefill text to NOT contaminate refusal text, got %s", body)
	}
	if !strings.Contains(body, "I'm not able to help with that request.") {
		t.Fatalf("expected refusal message in body, got %s", body)
	}
	if !strings.Contains(body, `"stop_reason":"refusal"`) {
		t.Fatalf("expected stop_reason refusal, got %s", body)
	}
}

func TestFSM_HandleToolCall_ClosesThinkingWithEffectiveSig(t *testing.T) {
	p := basicParams()
	p.PrefillText = "prefix "
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})
	_ = f.HandleThinking("some reasoning", "")
	if err := f.HandleFunctionCall("Bash", `{"command":"ls"}`, "effective-sig-xyz"); err != nil {
		t.Fatalf("HandleFunctionCall: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"signature":"effective-sig-xyz"`) {
		t.Fatalf("expected thinking block to close with effective-sig-xyz, got %s", body)
	}
}

func TestEnsureMessageStart_OutputTokensOne(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{EstInputTokens: 100})
	if err := f.EnsureMessageStart(); err != nil {
		t.Fatalf("EnsureMessageStart: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"output_tokens":1`) {
		t.Fatalf("expected message_start to report output_tokens: 1 to satisfy strict client validators, got %s", body)
	}
}

func TestProcessGeminiChunk_TrailingUsageMetadataAfterFinished(t *testing.T) {
	p := basicParams()
	p.SessionID = "sess_trailing_usage"
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{EstInputTokens: 50})

	// Chunk 1: finishReason without usageMetadata buffers pending finish so that
	// trailing usageMetadata from a split chunk can be included in message_delta
	finishChunk := []byte(`{"candidates":[{"content":{"parts":[{"text":"Done"}]},"finishReason":"STOP"}]}`)
	if err := f.ProcessGeminiChunk(finishChunk); err != nil {
		t.Fatalf("ProcessGeminiChunk finish: %v", err)
	}

	// Chunk 2: trailing chunk with cumulative usageMetadata completes the stream with real usage
	usageChunk := []byte(`{"usageMetadata":{"promptTokenCount":250,"candidatesTokenCount":120,"cachedContentTokenCount":180}}`)
	if err := f.ProcessGeminiChunk(usageChunk); err != nil {
		t.Fatalf("ProcessGeminiChunk trailing usage: %v", err)
	}

	// Verify stream marked as finished once usage chunk completes it
	f.mu.Lock()
	finished := f.hasFinished
	f.mu.Unlock()
	if !finished {
		t.Fatal("expected stream to be finished after usageMetadata completes pending finish")
	}

	stats := f.Stats()
	if stats.InputTokens != 250 {
		t.Fatalf("expected InputTokens 250 from trailing chunk, got %d", stats.InputTokens)
	}
	if stats.OutputTokens != 120 {
		t.Fatalf("expected OutputTokens 120 from trailing chunk, got %d", stats.OutputTokens)
	}
	if stats.CacheTokens != 180 {
		t.Fatalf("expected CacheTokens 180 from trailing chunk, got %d", stats.CacheTokens)
	}

	frac, ok := getSessionCacheFraction("sess_trailing_usage")
	if !ok {
		t.Fatal("expected session cache fraction to be recorded")
	}
	expectedFrac := 180.0 / 250.0
	if frac != expectedFrac {
		t.Fatalf("expected session cache fraction %v, got %v", expectedFrac, frac)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"output_tokens":120`) {
		t.Fatalf("expected message_delta to carry real output_tokens 120 from trailing chunk, got:\n%s", body)
	}
}

func TestProcessGeminiChunk_CrossChunkThoughtSignatureBuffering(t *testing.T) {
	p := basicParams()
	p.SessionID = "sess_cross_chunk_sig"
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	// Chunk 1: reasoning with thought: true, no signature yet
	chunk1 := []byte(`{"candidates":[{"content":{"parts":[{"thought":true,"text":"Thinking..."}]}}]}`)
	if err := f.ProcessGeminiChunk(chunk1); err != nil {
		t.Fatalf("chunk1: %v", err)
	}

	// Chunk 2: text delta closes thinking block, but has no signature yet
	chunk2 := []byte(`{"candidates":[{"content":{"parts":[{"text":"Here is the answer"}]}}]}`)
	if err := f.ProcessGeminiChunk(chunk2); err != nil {
		t.Fatalf("chunk2: %v", err)
	}

	bodySoFar := rec.Body.String()
	// Strict Anthropic SSE event ordering: signature_delta MUST precede content_block_stop
	stopIdx := strings.Index(bodySoFar, `"type":"content_block_stop"`)
	sigIdx := strings.Index(bodySoFar, `"type":"signature_delta"`)
	if sigIdx == -1 || stopIdx == -1 || sigIdx > stopIdx {
		t.Fatalf("signature_delta must be emitted strictly BEFORE content_block_stop, got: %s", bodySoFar)
	}

	// Chunk 3: functionCall in subsequent chunk carries the real thoughtSignature
	chunk3 := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"Bash","args":{"command":"ls"}},"thoughtSignature":"real-sig-cross-chunk-123"}]}}]}`)
	if err := f.ProcessGeminiChunk(chunk3); err != nil {
		t.Fatalf("chunk3: %v", err)
	}

	// Verify session signature is updated for history replay
	sessSig := toolsig.GetSessionSignature("sess_cross_chunk_sig")
	if sessSig != "real-sig-cross-chunk-123" {
		t.Fatalf("expected session signature 'real-sig-cross-chunk-123', got '%s'", sessSig)
	}

	// Verify that no content_block_delta for index 0 was emitted after content_block_stop for index 0
	bodyFinal := rec.Body.String()
	afterStop := bodyFinal[stopIdx+len(`"type":"content_block_stop"`):]
	if strings.Contains(afterStop, `"index":0`) {
		t.Fatalf("no delta for index 0 allowed after index 0 stop, got: %s", afterStop)
	}
}

func TestProcessGeminiChunk_LookAheadCapturesThoughtSignatureFromLaterPart(t *testing.T) {
	p := basicParams()
	p.SessionID = "sess_lookahead_sig"
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	chunk := []byte(`{"candidates":[{"content":{"parts":[
			{"thought":true,"text":"Analyzing task..."},
			{"text":"I will run pwd now."},
			{"functionCall":{"name":"Bash","args":{"command":"pwd"}},"thoughtSignature":"real-sig-lookahead-789"}
		]}}]}`)
	if err := f.ProcessGeminiChunk(chunk); err != nil {
		t.Fatalf("ProcessGeminiChunk: %v", err)
	}

	body := rec.Body.String()
	// Thinking block 0 must be closed with real signature, NOT skip_thought_signature_validator
	if strings.Contains(body, "skip_thought_signature_validator") {
		t.Fatalf("expected real signature to be used via look-ahead, got skip placeholder in: %s", body)
	}
	if !strings.Contains(body, `"signature":"real-sig-lookahead-789"`) {
		t.Fatalf("expected real-sig-lookahead-789 in body, got: %s", body)
	}
	sessSig := toolsig.GetSessionSignature("sess_lookahead_sig")
	if sessSig != "real-sig-lookahead-789" {
		t.Fatalf("expected session signature 'real-sig-lookahead-789', got %q", sessSig)
	}
}

func TestProcessGeminiChunk_CrossChunkThoughtSignatureFallbackOnFinish(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	// Chunk 1: reasoning without signature
	chunk1 := []byte(`{"candidates":[{"content":{"parts":[{"thought":true,"text":"Thinking..."}]}}]}`)
	if err := f.ProcessGeminiChunk(chunk1); err != nil {
		t.Fatalf("chunk1: %v", err)
	}

	// Chunk 2: text closes thinking block without signature
	chunk2 := []byte(`{"candidates":[{"content":{"parts":[{"text":"Just text"}]}}]}`)
	if err := f.ProcessGeminiChunk(chunk2); err != nil {
		t.Fatalf("chunk2: %v", err)
	}

	// Finish stream without any subsequent tool call or signature
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}

	body := rec.Body.String()
	// Fallback should be emitted on finish when no signature arrived
	if !strings.Contains(body, `"signature":"skip_thought_signature_validator"`) {
		t.Fatalf("expected fallback signature_delta on stream finish, got: %s", body)
	}
}

func TestPrependPrefillOnce_UTF8Cyrillic_NeverSplitMidRune(t *testing.T) {
	// First byte of Cyrillic 'В' (0xD0 0x92) and 'А' (0xD0 0x90) is identical (0xD0).
	// A naive byte-level overlap check would match byte 0 and slice mid-rune.
	emitted := false
	got := PrependPrefillOnce("В", "А", &emitted)
	if got != "ВА" {
		t.Fatalf("expected 'ВА', got %q", got)
	}

	// Multi-word Cyrillic overlap on whole word boundary
	emitted2 := false
	got2 := PrependPrefillOnce("Вот ответ: ", "Вот что я думаю", &emitted2)
	if got2 != "Вот ответ: что я думаю" {
		t.Fatalf("expected 'Вот ответ: что я думаю', got %q", got2)
	}
}

func TestPrependPrefillOnce_WordBoundaryPrefix_AvoidsWordMangling(t *testing.T) {
	// "The" and "There" share 3 bytes ("The"), but "The" is not a whole word in "There".
	// The overlap check must not slice "There" into "re".
	emitted := false
	got := PrependPrefillOnce("The total count is ", "There are 5 items", &emitted)
	if got != "The total count is There are 5 items" {
		t.Fatalf("expected 'The total count is There are 5 items', got %q", got)
	}
}

func TestFinishStream_OutputTokensEstimatedWhenZeroAndTextGenerated(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	_ = f.HandleText("This is 32 characters of output.")
	// Finish without usageMetadata from Gemini
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}

	body := rec.Body.String()
	// Expected estimated output tokens: 32 / 4 = 8 tokens
	if !strings.Contains(body, `"output_tokens":8`) {
		t.Fatalf("expected output_tokens to be estimated as 8, got: %s", body)
	}
}

func TestFinishStream_MessageDeltaUsageOnlyContainsOutputTokens(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	usageChunk := []byte(`{"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":25,"cachedContentTokenCount":40}}`)
	if err := f.ProcessGeminiChunk(usageChunk); err != nil {
		t.Fatalf("ProcessGeminiChunk: %v", err)
	}
	_ = f.HandleText("Done")
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}

	body := rec.Body.String()
	// Find the message_delta event line
	var deltaJSON string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if gjson.Get(payload, "type").String() == "message_delta" {
				deltaJSON = payload
				break
			}
		}
	}
	if deltaJSON == "" {
		t.Fatalf("message_delta event not found in body: %s", body)
	}

	usage := gjson.Get(deltaJSON, "usage")
	if !usage.Exists() {
		t.Fatalf("usage object missing in message_delta: %s", deltaJSON)
	}
	if usage.Get("output_tokens").Int() != 25 {
		t.Fatalf("expected output_tokens 25, got %v", usage.Get("output_tokens"))
	}
	if usage.Get("input_tokens").Exists() {
		t.Fatalf("message_delta.usage must strictly NOT contain input_tokens, got: %s", deltaJSON)
	}
	if usage.Get("cache_creation_input_tokens").Exists() {
		t.Fatalf("message_delta.usage must strictly NOT contain cache_creation_input_tokens, got: %s", deltaJSON)
	}
	if usage.Get("cache_read_input_tokens").Exists() {
		t.Fatalf("message_delta.usage must strictly NOT contain cache_read_input_tokens, got: %s", deltaJSON)
	}
}

func TestProcessGeminiChunk_CandidateLevelThoughtSignatureLookahead(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	// Chunk 1: reasoning without signature
	chunk1 := []byte(`{"candidates":[{"content":{"parts":[{"thought":true,"text":"Analyzing..."}]}}]}`)
	if err := f.ProcessGeminiChunk(chunk1); err != nil {
		t.Fatalf("chunk1: %v", err)
	}

	// Chunk 2: text part with candidate-level thoughtSignature
	chunk2 := []byte(`{"candidates":[{"thoughtSignature":"cand-sig-abc-123","content":{"parts":[{"text":"Result here"}]}}]}`)
	if err := f.ProcessGeminiChunk(chunk2); err != nil {
		t.Fatalf("chunk2: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"signature":"cand-sig-abc-123"`) {
		t.Fatalf("expected signature_delta to carry 'cand-sig-abc-123', got: %s", body)
	}
	if strings.Contains(body, `"signature":"skip_thought_signature_validator"`) {
		t.Fatalf("must not emit fallback skip validator when candidate carries signature: %s", body)
	}
}

func TestProcessGeminiChunk_SessionCachedThoughtSignatureUsedOnThinkingClose(t *testing.T) {
	p := basicParams()
	p.SessionID = "sess_cached_test_456"
	toolsig.SetSessionSignature(p.SessionID, "real-cached-session-sig")

	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	// Chunk 1: reasoning without signature
	chunk1 := []byte(`{"candidates":[{"content":{"parts":[{"thought":true,"text":"Thinking..."}]}}]}`)
	if err := f.ProcessGeminiChunk(chunk1); err != nil {
		t.Fatalf("chunk1: %v", err)
	}

	// Chunk 2: text delta closes thinking block without chunk-level signature
	chunk2 := []byte(`{"candidates":[{"content":{"parts":[{"text":"Here is the answer"}]}}]}`)
	if err := f.ProcessGeminiChunk(chunk2); err != nil {
		t.Fatalf("chunk2: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"signature":"real-cached-session-sig"`) {
		t.Fatalf("expected signature_delta to use cached session signature, got: %s", body)
	}
	if strings.Contains(body, `"signature":"skip_thought_signature_validator"`) {
		t.Fatalf("must not fall back to skip validator when session has cached signature: %s", body)
	}
}

// GAP-05: 1-character or short accidental prefix match must not drop the rest of prefill
func TestPrependPrefillOnce_ShortAccidentalPrefix_FullPrefillPrepended(t *testing.T) {
	emitted := false
	// Prefill is JSON object start, model starts with '{' and continues with different key
	got := PrependPrefillOnce(`{"status": "ok", `, `{`, &emitted)
	if got != `{"status": "ok", ` {
		t.Fatalf("expected full prefill preserved, got %q", got)
	}
}

// GAP-16: Thinking-only turn must emit a text block with prefill or fallback to satisfy minItems: 1
func TestFSM_FinishStream_ThinkingOnly_EmitsTextWithPrefill(t *testing.T) {
	p := basicParams()
	p.PrefillText = "Summary: "
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	if err := f.HandleThinking("Deep reasoning here...", "sig123"); err != nil {
		t.Fatalf("HandleThinking: %v", err)
	}
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}

	body := rec.Body.String()
	// Must contain thinking block (index 0) AND text block (index 1) with prefill
	if !strings.Contains(body, `"type":"thinking"`) {
		t.Fatalf("expected thinking block, got: %s", body)
	}
	if !strings.Contains(body, `"type":"text"`) || !strings.Contains(body, "Summary: ") {
		t.Fatalf("expected text block with prefill text, got: %s", body)
	}
}

// GAP-05 Deep Edge Cases: 1-2 byte initial chunk echoes (e.g. "{", "H", "{\"")
func TestGAP05_PrefillEdgeCases_1to2ByteChunks(t *testing.T) {
	// Case 1: 1-byte "{" with JSON prefill
	emitted := false
	got1 := PrependPrefillOnce(`{"name": "Alice", "active": true}`, `{`, &emitted)
	if got1 != `{"name": "Alice", "active": true}` {
		t.Fatalf("Case 1: expected full prefill preserved, got %q", got1)
	}

	// Case 2: 2-byte "{\"" with JSON prefill
	emitted = false
	got2 := PrependPrefillOnce(`{"name": "Alice", "active": true}`, `{"`, &emitted)
	if got2 != `{"name": "Alice", "active": true}` {
		t.Fatalf("Case 2: expected full prefill preserved, got %q", got2)
	}

	// Case 3: 1-byte "H" with text prefill
	emitted = false
	got3 := PrependPrefillOnce("Hello world", "H", &emitted)
	if !strings.HasPrefix(got3, "Hello world") {
		t.Fatalf("Case 3: expected full prefill preserved at start, got %q", got3)
	}

	// Case 4: 2-byte "He" with text prefill
	emitted = false
	got4 := PrependPrefillOnce("Hello world", "He", &emitted)
	if !strings.HasPrefix(got4, "Hello world") {
		t.Fatalf("Case 4: expected full prefill preserved at start, got %q", got4)
	}

	// Case 5: 3-byte valid echo with boundary (e.g. "```")
	emitted = false
	got5 := PrependPrefillOnce("```json\n{}", "```", &emitted)
	if got5 != "```" {
		t.Fatalf("Case 5: substantial echo should be returned as-is, got %q", got5)
	}
}

// GAP-16 Deep Invariants: Zero content, Thinking-only, Tool-only, and Refusal
func TestGAP16_FinishStream_AllCombinations(t *testing.T) {
	// Subtest A: Completely empty turn (currentIndex < 0) with prefill
	t.Run("EmptyWithPrefill", func(t *testing.T) {
		p := basicParams()
		p.PrefillText = "Prefill: "
		rec := httptest.NewRecorder()
		w := NewSafeSSEWriter(1, rec, rec, nil)
		f := NewFSM(w, 1, p, &reqctx.State{})

		if err := f.FinishStream("STOP"); err != nil {
			t.Fatalf("FinishStream: %v", err)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"type":"text"`) || !strings.Contains(body, "Prefill: ") {
			t.Fatalf("expected text block with prefill, got %s", body)
		}
		if !strings.Contains(body, `"index":0`) {
			t.Fatalf("expected block index 0, got %s", body)
		}
	})

	// Subtest B: Completely empty turn without prefill -> "(empty response)"
	t.Run("EmptyWithoutPrefill", func(t *testing.T) {
		p := basicParams()
		rec := httptest.NewRecorder()
		w := NewSafeSSEWriter(1, rec, rec, nil)
		f := NewFSM(w, 1, p, &reqctx.State{})

		if err := f.FinishStream("STOP"); err != nil {
			t.Fatalf("FinishStream: %v", err)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "(empty response)") {
			t.Fatalf("expected '(empty response)', got %s", body)
		}
	})

	// Subtest C: Tool use only -> must NOT emit synthetic text block
	t.Run("ToolUseOnly_NoSyntheticText", func(t *testing.T) {
		p := basicParams()
		rec := httptest.NewRecorder()
		w := NewSafeSSEWriter(1, rec, rec, nil)
		f := NewFSM(w, 1, p, &reqctx.State{})

		if err := f.HandleFunctionCall("test_tool", `{"x":1}`, "sig"); err != nil {
			t.Fatalf("HandleFunctionCall: %v", err)
		}
		if err := f.FinishStream("STOP"); err != nil {
			t.Fatalf("FinishStream: %v", err)
		}
		body := rec.Body.String()
		if strings.Contains(body, "(empty response)") {
			t.Fatalf("tool_use-only must not contain synthetic empty response: %s", body)
		}
		if strings.Contains(body, `"type":"text"`) {
			t.Fatalf("tool_use-only must not contain text block: %s", body)
		}
	})

	// Subtest D: Refusal with zero content -> refusal message without prefill
	t.Run("RefusalZeroContent", func(t *testing.T) {
		p := basicParams()
		p.PrefillText = "My prefill: "
		rec := httptest.NewRecorder()
		w := NewSafeSSEWriter(1, rec, rec, nil)
		f := NewFSM(w, 1, p, &reqctx.State{})

		if err := f.FinishStream("SAFETY"); err != nil {
			t.Fatalf("FinishStream: %v", err)
		}
		body := rec.Body.String()
		if strings.Contains(body, "My prefill") {
			t.Fatalf("refusal must not contain user prefill: %s", body)
		}
		if !strings.Contains(body, "I'm not able to help with that request.") {
			t.Fatalf("expected refusal fallback text: %s", body)
		}
		if !strings.Contains(body, `"stop_reason":"refusal"`) {
			t.Fatalf("expected stop_reason refusal: %s", body)
		}
	})
}

// Invariant: Content block indices strictly monotonic (0, 1, 2...)
func TestFSM_StrictIndexMonotonicity_MultiTurn(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	// 1. Thinking block (index 0)
	if err := f.HandleThinking("Thinking step 1", "sig1"); err != nil {
		t.Fatalf("HandleThinking: %v", err)
	}

	// 2. Text block (index 1)
	if err := f.HandleText("Here is some text."); err != nil {
		t.Fatalf("HandleText: %v", err)
	}

	// 3. Tool call 1 (index 2)
	if err := f.HandleFunctionCall("tool_a", `{"a":1}`, "sig2"); err != nil {
		t.Fatalf("HandleFunctionCall 1: %v", err)
	}

	// 4. Tool call 2 (index 3)
	if err := f.HandleFunctionCall("tool_b", `{"b":2}`, "sig3"); err != nil {
		t.Fatalf("HandleFunctionCall 2: %v", err)
	}

	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}

	body := rec.Body.String()
	lines := strings.Split(body, "\n")

	type event struct {
		name string
		data string
	}
	var events []event
	var curEvent string
	for _, l := range lines {
		if strings.HasPrefix(l, "event: ") {
			curEvent = strings.TrimPrefix(l, "event: ")
		} else if strings.HasPrefix(l, "data: ") {
			events = append(events, event{name: curEvent, data: strings.TrimPrefix(l, "data: ")})
		}
	}

	// Track start and stop indices
	var startIndices []int
	var stopIndices []int
	for _, ev := range events {
		root := gjson.Parse(ev.data)
		switch root.Get("type").String() {
		case "content_block_start":
			startIndices = append(startIndices, int(root.Get("index").Int()))
		case "content_block_stop":
			stopIndices = append(stopIndices, int(root.Get("index").Int()))
		}
	}

	if len(startIndices) != 4 {
		t.Fatalf("expected 4 blocks started, got %d (%v)", len(startIndices), startIndices)
	}
	for i := 0; i < 4; i++ {
		if startIndices[i] != i {
			t.Fatalf("start index mismatch: expected %d, got %d", i, startIndices[i])
		}
		if stopIndices[i] != i {
			t.Fatalf("stop index mismatch: expected %d, got %d", i, stopIndices[i])
		}
	}
}

// Invariant: signature_delta strictly emitted BEFORE content_block_stop
func TestFSM_SignatureDeltaBeforeContentBlockStop(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	if err := f.HandleThinking("Deep reasoning", "sig_valid_123"); err != nil {
		t.Fatalf("HandleThinking: %v", err)
	}
	if err := f.HandleText("Answer text"); err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}

	body := rec.Body.String()
	sigIdx := strings.Index(body, `"type":"signature_delta"`)
	stopIdx := strings.Index(body, `"content_block_stop"`)

	if sigIdx == -1 {
		t.Fatalf("signature_delta not found in body: %s", body)
	}
	if stopIdx == -1 {
		t.Fatalf("content_block_stop for index 0 not found in body: %s", body)
	}
	if sigIdx >= stopIdx {
		t.Fatalf("signature_delta (%d) must precede content_block_stop (%d)", sigIdx, stopIdx)
	}
}

// Invariant: Tool calls with empty arguments formatted as "{}" in input_json_delta
func TestToolCalls_EmptyOrNullArgs_FormattedAsObject(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"empty string", ""},
		{"null literal", "null"},
		{"empty object", "{}"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := basicParams()
			rec := httptest.NewRecorder()
			w := NewSafeSSEWriter(1, rec, rec, nil)
			f := NewFSM(w, 1, p, &reqctx.State{})

			if err := f.HandleFunctionCall("test_tool", tc.args, "sig"); err != nil {
				t.Fatalf("HandleFunctionCall: %v", err)
			}
			body := rec.Body.String()
			if !strings.Contains(body, `"partial_json":"{}"`) {
				t.Fatalf("expected partial_json:\"{}\", got: %s", body)
			}
		})
	}
}

// Invariant: SafeSSEWriter prevents double WriteHeader and handles mid-stream errors
func TestSafeSSEWriter_MidStreamError_PreventsDoubleWriteHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)

	// 1. Emit an event (commits headers)
	if err := w.EmitEvent("ping", `{"type":"ping"}`); err != nil {
		t.Fatalf("EmitEvent: %v", err)
	}
	if !w.HeadersSent() {
		t.Fatal("expected HeadersSent() to be true")
	}

	// 2. Terminate with error mid-stream
	sentAsSSE := w.TerminateWithError("overloaded_error", "stream interrupted")
	if !sentAsSSE {
		t.Fatal("expected sentAsSSE=true after headers committed")
	}

	// 3. Second call to TerminateWithError must be a no-op
	secondCall := w.TerminateWithError("overloaded_error", "second error")
	if !secondCall {
		t.Fatal("second call should still report sentAsSSE=true")
	}

	body := rec.Body.String()
	if strings.Count(body, "event: error") != 1 {
		t.Fatalf("expected exactly 1 error event, got %d", strings.Count(body, "event: error"))
	}
}

// Invariant: Stop sequences are buffered and stripped from outbound text_delta
func TestFSM_StopSequences_HoldBackBufferStripsStopSequenceFromWire(t *testing.T) {
	p := basicParams()
	p.StopSequences = []string{"STOP", "###"}
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	if err := f.HandleText("Result text "); err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	if err := f.HandleText("ST"); err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	if err := f.HandleText("OP"); err != nil {
		t.Fatalf("HandleText: %v", err)
	}

	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, `"text":"STOP"`) || strings.Contains(body, `"text":"Result text STOP"`) {
		t.Fatalf("stop sequence must not be emitted to client in text_delta: %s", body)
	}
	if !strings.Contains(body, `"stop_reason":"stop_sequence"`) {
		t.Fatalf("expected stop_reason to be stop_sequence: %s", body)
	}
	if !strings.Contains(body, `"stop_sequence":"STOP"`) {
		t.Fatalf("expected stop_sequence to be 'STOP': %s", body)
	}
}

func TestFSM_SplitSyntheticXMLTags_NeverLeakAcrossChunks(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, basicParams(), &reqctx.State{})

	// Chunk 1: regular text ending with a split tag prefix
	if err := f.HandleText("Normal text before </task-"); err != nil {
		t.Fatalf("HandleText 1: %v", err)
	}
	body1 := rec.Body.String()
	// Must have emitted the prefix "Normal text before " but held back "</task-"
	if strings.Contains(body1, "</task-") {
		t.Fatalf("partial tag prefix must be held back, got: %s", body1)
	}

	// Chunk 2: the rest of the tag followed by more normal text
	if err := f.HandleText("notification> and normal text after"); err != nil {
		t.Fatalf("HandleText 2: %v", err)
	}
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}

	fullBody := rec.Body.String()
	if strings.Contains(fullBody, "task-notification") || strings.Contains(fullBody, "</task-") {
		t.Fatalf("synthetic tag must be completely stripped across chunk boundaries, got: %s", fullBody)
	}
	if !strings.Contains(fullBody, "Normal text before ") || !strings.Contains(fullBody, " and normal text after") {
		t.Fatalf("expected normal text preserved, got: %s", fullBody)
	}
}

// Adversarial test: finishReason: "STOP" without usageMetadata, followed by [DONE]
func TestStreamGeminiToClaude_PendingFinishReason_TrailingDone(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{EstInputTokens: 50})

	sseStream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"All finished.\"}]},\"finishReason\":\"STOP\"}]}\n\ndata: [DONE]\n\n"
	err := StreamGeminiToClaude(f, strings.NewReader(sseStream), nil)
	if err != nil {
		t.Fatalf("StreamGeminiToClaude: %v", err)
	}

	if !f.HasFinished() {
		t.Fatal("expected FSM to be finished after [DONE]")
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected stop_reason:end_turn, got: %s", body)
	}
	if !strings.Contains(body, `"type":"message_stop"`) {
		t.Fatalf("expected message_stop event, got: %s", body)
	}
	// Verify message_delta usage does NOT have input_tokens
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "data: ") && strings.Contains(l, "message_delta") {
			payload := strings.TrimPrefix(l, "data: ")
			if gjson.Get(payload, "usage.input_tokens").Exists() {
				t.Fatalf("message_delta must not contain input_tokens: %s", payload)
			}
			if !gjson.Get(payload, "usage.output_tokens").Exists() {
				t.Fatalf("message_delta must contain output_tokens: %s", payload)
			}
		}
	}
}

// Adversarial test: finishReason: "STOP" without usageMetadata, followed by EOF (no [DONE])
func TestStreamGeminiToClaude_PendingFinishReason_TrailingEOF(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{EstInputTokens: 50})

	sseStream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"EOF content\"}]},\"finishReason\":\"STOP\"}]}\n\n"
	err := StreamGeminiToClaude(f, strings.NewReader(sseStream), nil)
	if err != nil {
		t.Fatalf("StreamGeminiToClaude: %v", err)
	}

	if !f.HasFinished() {
		t.Fatal("expected FSM to be finished after EOF")
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected stop_reason:end_turn on EOF, got: %s", body)
	}
	if !strings.Contains(body, `"type":"message_stop"`) {
		t.Fatalf("expected message_stop event on EOF, got: %s", body)
	}
}

// Adversarial test: finishReason without usageMetadata, followed by in-band error chunk
func TestStreamGeminiToClaude_PendingFinishReason_InterveningInBandError(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{EstInputTokens: 50})

	sseStream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Partial\"}]},\"finishReason\":\"STOP\"}]}\n\n" +
		"data: {\"error\":{\"code\":429,\"message\":\"Quota exceeded\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n"
	err := StreamGeminiToClaude(f, strings.NewReader(sseStream), nil)
	if err == nil {
		t.Fatal("expected error from in-band error chunk, got nil")
	}

	if !f.HasFinished() {
		t.Fatal("expected FSM to be finished after error")
	}

	body := rec.Body.String()
	// Must contain content_block_stop closing the open text block
	if !strings.Contains(body, `"type":"content_block_stop"`) {
		t.Fatalf("expected content_block_stop to close open block before error, got: %s", body)
	}
	// Must contain error event
	if !strings.Contains(body, "event: error") || !strings.Contains(body, "rate_limit_error") {
		t.Fatalf("expected event: error with rate_limit_error, got: %s", body)
	}
	// Must strictly NOT contain message_stop or message_delta
	if strings.Contains(body, `"type":"message_stop"`) {
		t.Fatalf("must NOT emit message_stop on error, got: %s", body)
	}
	if strings.Contains(body, `"type":"message_delta"`) {
		t.Fatalf("must NOT emit message_delta on error, got: %s", body)
	}
}

// Adversarial test: finishReason: "MAX_TOKENS" without usageMetadata, followed by [DONE]
func TestStreamGeminiToClaude_PendingFinishReason_MaxTokensWithTrailingDone(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{EstInputTokens: 50})

	sseStream := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Clipped answer\"}]},\"finishReason\":\"MAX_TOKENS\"}]}\n\ndata: [DONE]\n\n"
	err := StreamGeminiToClaude(f, strings.NewReader(sseStream), nil)
	if err != nil {
		t.Fatalf("StreamGeminiToClaude: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"stop_reason":"max_tokens"`) {
		t.Fatalf("expected stop_reason:max_tokens, got: %s", body)
	}
}

// Adversarial test: Split buffer chunk across read boundaries in StreamGeminiToClaude
func TestStreamGeminiToClaude_SplitBufferChunk(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{EstInputTokens: 50})

	raw := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Chunked read test\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":15}}\n\ndata: [DONE]\n\n"
	slowReader := &tinyChunkReader{data: []byte(raw), chunkSize: 12}

	err := StreamGeminiToClaude(f, slowReader, nil)
	if err != nil {
		t.Fatalf("StreamGeminiToClaude: %v", err)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "Chunked read test") {
		t.Fatalf("expected text to be reassembled across split reads, got: %s", body)
	}
	if !strings.Contains(body, `"output_tokens":15`) {
		t.Fatalf("expected real output_tokens 15, got: %s", body)
	}
}

type tinyChunkReader struct {
	data      []byte
	pos       int
	chunkSize int
}

func (r *tinyChunkReader) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	remaining := len(r.data) - r.pos
	toRead := r.chunkSize
	if toRead > remaining {
		toRead = remaining
	}
	if toRead > len(p) {
		toRead = len(p)
	}
	copy(p, r.data[r.pos:r.pos+toRead])
	r.pos += toRead
	return toRead, nil
}

// Adversarial test: After tool_use, any subsequent text is dropped
func TestFSM_AfterToolUse_NoTextEmitted(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	if err := f.HandleFunctionCall("test_tool", `{"k":"v"}`, "sig"); err != nil {
		t.Fatalf("HandleFunctionCall: %v", err)
	}
	// Gemini sends text after tool_use
	if err := f.HandleText("This text must be dropped"); err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}

	body := rec.Body.String()
	if strings.Contains(body, "This text must be dropped") {
		t.Fatalf("text after tool_use must NEVER be emitted: %s", body)
	}
	if !strings.Contains(body, `"stop_reason":"tool_use"`) {
		t.Fatalf("expected stop_reason:tool_use, got: %s", body)
	}
}

// Adversarial test: FinishWithError closes open thinking block and does NOT send message_stop
func TestFSM_FinishWithError_ClosesOpenBlocksAndNoMessageStop(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{})

	if err := f.HandleThinking("Deep pondering...", "sig_open"); err != nil {
		t.Fatalf("HandleThinking: %v", err)
	}

	// Mid-thinking network drop
	f.FinishWithError(errors.New("connection reset by peer"))

	if !f.HasFinished() {
		t.Fatal("expected HasFinished()=true after FinishWithError")
	}

	body := rec.Body.String()
	// Must close thinking block: signature_delta THEN content_block_stop
	sigIdx := strings.Index(body, `"type":"signature_delta"`)
	stopIdx := strings.Index(body, `"type":"content_block_stop"`)
	errIdx := strings.Index(body, "event: error")

	if sigIdx == -1 || stopIdx == -1 || errIdx == -1 {
		t.Fatalf("expected signature_delta, content_block_stop, and event: error in body: %s", body)
	}
	if sigIdx >= stopIdx {
		t.Fatalf("signature_delta must precede content_block_stop")
	}
	if stopIdx >= errIdx {
		t.Fatalf("content_block_stop must precede event: error")
	}
	if strings.Contains(body, `"type":"message_stop"`) {
		t.Fatalf("message_stop must NEVER be sent on error: %s", body)
	}

	// Calling FinishWithError second time must be a silent no-op
	f.FinishWithError(errors.New("second error"))
	secondBody := rec.Body.String()
	if strings.Count(secondBody, "event: error") != 1 {
		t.Fatalf("second FinishWithError must be no-op, got %d error events", strings.Count(secondBody, "event: error"))
	}
}

// -----------------------------------------------------------------------------
// Prompt Caching Emulation Audit Tests
// -----------------------------------------------------------------------------

func parseEventPayload(body, eventType string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if gjson.Get(payload, "type").String() == eventType {
				return payload
			}
		}
	}
	return ""
}

func TestPromptCaching_SessionCacheFraction_MultiTurnProgression(t *testing.T) {
	sessionID := "sess_caching_audit_turn_flow"

	// --- Turn 1: Fresh conversation ---
	{
		p1 := basicParams()
		p1.SessionID = sessionID
		p1.HasPriorTurns = false
		st1 := &reqctx.State{EstInputTokens: 3000, CachePrefixFrac: 0.80}
		rec1 := httptest.NewRecorder()
		w1 := NewSafeSSEWriter(1, rec1, rec1, nil)
		f1 := NewFSM(w1, 1, p1, st1)

		if err := f1.EnsureMessageStart(); err != nil {
			t.Fatalf("Turn 1 EnsureMessageStart: %v", err)
		}

		payload1 := parseEventPayload(rec1.Body.String(), "message_start")
		inTok1 := gjson.Get(payload1, "message.usage.input_tokens").Int()
		cachedTok1 := gjson.Get(payload1, "message.usage.cache_read_input_tokens").Int()
		creationTok1 := gjson.Get(payload1, "message.usage.cache_creation_input_tokens").Int()

		if cachedTok1 != 0 {
			t.Errorf("Turn 1 message_start cache_read: got %d, want 0", cachedTok1)
		}
		if creationTok1 != 2400 {
			t.Errorf("Turn 1 message_start cache_creation: got %d, want 2400", creationTok1)
		}
		if inTok1 != 600 {
			t.Errorf("Turn 1 message_start uncached input: got %d, want 600", inTok1)
		}
		if inTok1+cachedTok1+creationTok1 != 3000 {
			t.Errorf("Turn 1 conservation violated: %d + %d + %d != 3000", inTok1, cachedTok1, creationTok1)
		}

		f1.inputTokens = 3000
		f1.cacheTokens = 0
		if err := f1.FinishStream("STOP"); err != nil {
			t.Fatalf("Turn 1 FinishStream: %v", err)
		}

		frac, ok := getSessionCacheFraction(sessionID)
		if !ok || frac != 0.85 {
			t.Fatalf("Turn 1 expected seeded fraction 0.85, got %v (ok=%v)", frac, ok)
		}
	}

	// --- Turn 2: Second turn ---
	{
		p2 := basicParams()
		p2.SessionID = sessionID
		p2.HasPriorTurns = true
		st2 := &reqctx.State{EstInputTokens: 3200, CachePrefixFrac: 0}
		rec2 := httptest.NewRecorder()
		w2 := NewSafeSSEWriter(2, rec2, rec2, nil)
		f2 := NewFSM(w2, 2, p2, st2)

		if err := f2.EnsureMessageStart(); err != nil {
			t.Fatalf("Turn 2 EnsureMessageStart: %v", err)
		}

		payload2 := parseEventPayload(rec2.Body.String(), "message_start")
		inTok2 := gjson.Get(payload2, "message.usage.input_tokens").Int()
		cachedTok2 := gjson.Get(payload2, "message.usage.cache_read_input_tokens").Int()
		creationTok2 := gjson.Get(payload2, "message.usage.cache_creation_input_tokens").Int()

		if cachedTok2 != 2720 {
			t.Errorf("Turn 2 message_start cache_read: got %d, want 2720", cachedTok2)
		}
		if inTok2 != 480 {
			t.Errorf("Turn 2 message_start uncached input: got %d, want 480", inTok2)
		}
		if inTok2+cachedTok2+creationTok2 != 3200 {
			t.Errorf("Turn 2 conservation violated: %d + %d + %d != 3200", inTok2, cachedTok2, creationTok2)
		}

		f2.inputTokens = 3200
		f2.cacheTokens = 2600
		if err := f2.FinishStream("STOP"); err != nil {
			t.Fatalf("Turn 2 FinishStream: %v", err)
		}

		frac, ok := getSessionCacheFraction(sessionID)
		expectedFrac := 2600.0 / 3200.0
		if !ok || frac != expectedFrac {
			t.Fatalf("Turn 2 expected real fraction %v, got %v", expectedFrac, frac)
		}
	}

	// --- Turn 3: Third turn ---
	{
		p3 := basicParams()
		p3.SessionID = sessionID
		p3.HasPriorTurns = true
		rec3 := httptest.NewRecorder()
		w3 := NewSafeSSEWriter(3, rec3, rec3, nil)
		f3 := NewFSM(w3, 3, p3, &reqctx.State{EstInputTokens: 4000})

		if err := f3.EnsureMessageStart(); err != nil {
			t.Fatalf("Turn 3 EnsureMessageStart: %v", err)
		}

		payload3 := parseEventPayload(rec3.Body.String(), "message_start")
		inTok3 := gjson.Get(payload3, "message.usage.input_tokens").Int()
		cachedTok3 := gjson.Get(payload3, "message.usage.cache_read_input_tokens").Int()

		if cachedTok3 != 3250 {
			t.Errorf("Turn 3 message_start cache_read: got %d, want 3250", cachedTok3)
		}
		if inTok3 != 750 {
			t.Errorf("Turn 3 message_start uncached input: got %d, want 750", inTok3)
		}
		if inTok3+cachedTok3 != 4000 {
			t.Errorf("Turn 3 conservation violated: %d + %d != 4000", inTok3, cachedTok3)
		}
	}
}

func TestPromptCaching_NoDoubleCountingInClaudeCode(t *testing.T) {
	p := basicParams()
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, p, &reqctx.State{EstInputTokens: 1500})

	f.inputTokens = 1500
	f.outputTokens = 120
	f.cacheTokens = 1000

	if err := f.HandleText("Done"); err != nil {
		t.Fatalf("HandleText: %v", err)
	}
	if err := f.FinishStream("STOP"); err != nil {
		t.Fatalf("FinishStream: %v", err)
	}

	body := rec.Body.String()
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "data: ") && strings.Contains(line, "message_delta") {
			payload := strings.TrimPrefix(line, "data: ")
			u := gjson.Get(payload, "usage")
			if u.Get("input_tokens").Exists() {
				t.Fatalf("message_delta must NOT contain input_tokens, found: %s", u.Raw)
			}
			if u.Get("cache_read_input_tokens").Exists() {
				t.Fatalf("message_delta must NOT contain cache_read_input_tokens, found: %s", u.Raw)
			}
			if u.Get("cache_creation_input_tokens").Exists() {
				t.Fatalf("message_delta must NOT contain cache_creation_input_tokens, found: %s", u.Raw)
			}
			if u.Get("output_tokens").Int() != 120 {
				t.Fatalf("expected output_tokens 120, got: %s", u.Raw)
			}
		}
	}
}


