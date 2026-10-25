package stream

import (
	"strings"
	"sync"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/reqctx"
)

type covRecorder struct {
	mu          sync.Mutex
	chunks, raw [][]byte
}

func (r *covRecorder) AppendGeminiChunk(_ uint64, c []byte) {
	r.mu.Lock()
	r.chunks = append(r.chunks, c)
	r.mu.Unlock()
}

func (r *covRecorder) AppendRawGeminiChunk(_ uint64, c []byte) {
	r.mu.Lock()
	r.raw = append(r.raw, c)
	r.mu.Unlock()
}

func TestCovChunkRecorderSeesChunksBeforeAndAfterFinish(t *testing.T) {
	f, _, _ := heldFSM(&reqctx.Params{ModelName: "claude-opus-4-5", SessionID: "cov-rec-sess"})
	rec := &covRecorder{}
	f.SetChunkRecorder(rec)
	if err := f.ProcessGeminiChunk(chunk(`{"text":"hi"}`, "STOP")); err != nil {
		t.Fatal(err)
	}
	if !f.HasFinished() {
		t.Fatal("expected finished")
	}
	// A late chunk in the same buffer: recorded, state untouched, usage applied.
	late := []byte(`{"response":{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":40}},"candidates":[]}`)
	if err := f.ProcessGeminiChunk(late); err != nil {
		t.Fatal(err)
	}
	if len(rec.chunks) != 2 || len(rec.raw) != 2 {
		t.Fatalf("recorder saw %d/%d chunks", len(rec.chunks), len(rec.raw))
	}
	if f.cacheTokens != 40 {
		t.Fatalf("late usage not applied, cacheTokens=%d", f.cacheTokens)
	}
	frac, ok := getSessionCacheFraction("cov-rec-sess")
	if !ok || frac != 0.4 {
		t.Fatalf("session cache fraction = %v %v want 0.4", frac, ok)
	}
}

func TestCovPromptFeedbackBlockEndsAsRefusal(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	_ = f.ProcessGeminiChunk([]byte(`{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT"}}`))
	if !f.HasFinished() {
		t.Fatal("blocked prompt must finish the stream")
	}
	if f.refusalDetail != "promptFeedback.blockReason=PROHIBITED_CONTENT" {
		t.Fatalf("refusalDetail=%q", f.refusalDetail)
	}
	if reason, _ := stopOf(rec.Body.String()); reason != "refusal" {
		t.Fatalf("stop_reason=%q body=%s", reason, rec.Body.String())
	}
}

func TestCovTrailingFinishReasonWithoutUsageDefersThenFinishes(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	c1 := []byte(`{"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}`)
	if err := f.ProcessGeminiChunk(c1); err != nil {
		t.Fatal(err)
	}
	if f.HasFinished() {
		t.Fatal("must wait for trailing usage")
	}
	c2 := []byte(`{"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3}}`)
	if err := f.ProcessGeminiChunk(c2); err != nil {
		t.Fatal(err)
	}
	if !f.HasFinished() {
		t.Fatal("empty-candidates chunk must flush the pending finish reason")
	}
	d := parseEventPayload(rec.Body.String(), "message_delta")
	if gjson.Get(d, "usage.output_tokens").Int() != 3 {
		t.Fatalf("usage not reflected: %s", d)
	}
}

func TestCovProcessOnePartThoughtSignatureOnlyChunk(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	if err := f.ProcessGeminiChunk(chunk(`{"thought":true,"text":"thinking"}`, "")); err != nil {
		t.Fatal(err)
	}
	// Trailing thought part with empty text but a signature: captured, no extra delta.
	before := strings.Count(rec.Body.String(), "thinking_delta")
	if err := f.ProcessGeminiChunk(chunk(`{"thought":true,"text":"","thoughtSignature":"SIGX"}`, "")); err != nil {
		t.Fatal(err)
	}
	if f.thoughtSignature != "SIGX" || f.lastTurnThoughtSignature != "SIGX" {
		t.Fatalf("signature not captured: %q %q", f.thoughtSignature, f.lastTurnThoughtSignature)
	}
	if strings.Count(rec.Body.String(), "thinking_delta") != before {
		t.Fatal("empty thought text must not emit a delta")
	}
}

func TestCovProcessOnePartFunctionCall(t *testing.T) {
	p := &reqctx.Params{
		ModelName:          "claude-opus-4-5",
		ReverseToolNameMap: map[string]string{"mcp_tool": "mcp-tool"},
	}
	f, rec, _ := heldFSM(p)
	// null args become {}, sanitized name restored, signature picked up from the part.
	err := f.ProcessGeminiChunk(chunk(`{"functionCall":{"name":"mcp_tool","args":null,"thoughtSignature":"FSIG"}}`, "STOP"))
	if err != nil {
		t.Fatal(err)
	}
	body := rec.Body.String()
	start := ""
	delta := ""
	for _, e := range parseEvents(body) {
		switch e.Name {
		case "content_block_start":
			start = e.Data
		case "content_block_delta":
			delta = e.Data
		}
	}
	if gjson.Get(start, "content_block.name").String() != "mcp-tool" {
		t.Fatalf("name not restored: %s", start)
	}
	if gjson.Get(delta, "delta.partial_json").String() != "{}" {
		t.Fatalf("null args must stream as {}: %s", delta)
	}
	if f.lastTurnThoughtSignature != "FSIG" {
		t.Fatalf("sig = %q", f.lastTurnThoughtSignature)
	}
	if reason, _ := stopOf(body); reason != "tool_use" {
		t.Fatalf("stop_reason=%q", reason)
	}
}

func TestCovTextPartAdoptsSignatureIntoOpenThinking(t *testing.T) {
	f, _, _ := heldFSM(basicParams())
	if err := f.ProcessGeminiChunk(chunk(`{"thought":true,"text":"t"}`, "")); err != nil {
		t.Fatal(err)
	}
	if f.thoughtSignature != "" {
		t.Fatal("precondition: no signature yet")
	}
	if err := f.processOnePart(gjson.Parse(`{"text":"answer","thoughtSignature":"TSIG"}`)); err != nil {
		t.Fatal(err)
	}
	if f.lastTurnThoughtSignature != "TSIG" {
		t.Fatalf("sig = %q", f.lastTurnThoughtSignature)
	}
}

func TestCovGroundingMetadataAndFooter(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	gm := `"groundingMetadata":{"webSearchQueries":["q1","q2"],"groundingChunks":[` +
		`{"web":{"uri":"https://a.example","title":"A"}},` +
		`{"web":{"uri":"https://b.example"}},` +
		`{"web":{"uri":"https://a.example","title":"A again"}},` +
		`{"web":{"title":"no uri"}}]}`
	c := `{"candidates":[{"content":{"parts":[{"text":"answer"}]},` + gm + `,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}`
	if err := f.ProcessGeminiChunk([]byte(c)); err != nil {
		t.Fatal(err)
	}
	if got := f.searchQueries; len(got) != 2 || got[0] != "q1" || got[1] != "q2" {
		t.Fatalf("queries = %v", got)
	}
	text := textOf(rec.Body.String())
	if !strings.Contains(text, "Google Search Sources") {
		t.Fatalf("footer missing: %q", text)
	}
	if !strings.Contains(text, "- [A](https://a.example)") || !strings.Contains(text, "- [https://b.example](https://b.example)") {
		t.Fatalf("sources wrong: %q", text)
	}
	if strings.Count(text, "https://a.example") != 1 {
		t.Fatalf("duplicate URI must be listed once: %q", text)
	}
	// non-existent metadata is a no-op
	f.collectGroundingMetadata(gjson.Result{})
}

func TestCovGroundingFooterRussianAndSkippedWithoutText(t *testing.T) {
	p := &reqctx.Params{ModelName: "claude-opus-4-5", PreferRussian: true}
	f, rec, _ := heldFSM(p)
	gm := `"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://r.example","title":"R"}}]}`
	c := `{"candidates":[{"content":{"parts":[{"text":"ответ"}]},` + gm + `,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2}}`
	if err := f.ProcessGeminiChunk([]byte(c)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(textOf(rec.Body.String()), "Источники Google Search") {
		t.Fatalf("russian footer missing: %q", textOf(rec.Body.String()))
	}

	f2, _, _ := heldFSM(basicParams())
	f2.groundingSources = []GroundingSource{{Title: "T", URI: "u"}}
	f2.appendGroundingSourcesFooter() // no text produced yet: nothing appended
	if len(f2.groundingSources) != 1 {
		t.Fatal("sources must stay pending when there is no answer text")
	}
}
