package orchestrate

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tidwall/gjson"
)

func covBlocks(bs ...map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"content": bs}
}

func covText(s string) map[string]interface{} {
	return map[string]interface{}{"type": "text", "text": s}
}

func TestCov_ApplyPrefillToResponse(t *testing.T) {
	// Empty prefill and non-block content are no-ops.
	r := covBlocks(covText("x"))
	applyPrefillToResponse(r, "")
	applyPrefillToResponse(map[string]interface{}{"content": "str"}, "pre")
	if r["content"].([]map[string]interface{})[0]["text"] != "x" {
		t.Fatal("no-op expected")
	}
	// Text block after a thinking block receives the prefix; only the first text block.
	r = covBlocks(
		map[string]interface{}{"type": "thinking", "thinking": "t"},
		covText("llo"), covText("second"),
	)
	applyPrefillToResponse(r, "He")
	bs := r["content"].([]map[string]interface{})
	if bs[1]["text"] != "Hello" || bs[2]["text"] != "second" {
		t.Fatalf("prefix must attach to first text block only: %v", bs)
	}
	// tool_use-only response: synthesized text block inserted before the tool_use.
	r = covBlocks(
		map[string]interface{}{"type": "thinking"},
		map[string]interface{}{"type": "tool_use", "name": "x"},
	)
	applyPrefillToResponse(r, "Pre")
	bs = r["content"].([]map[string]interface{})
	if len(bs) != 3 || bs[1]["type"] != "text" || bs[1]["text"] != "Pre" || bs[2]["type"] != "tool_use" {
		t.Fatalf("synthesized prefill block: %v", bs)
	}
	// No tool_use and no text: appended at the end.
	r = covBlocks(map[string]interface{}{"type": "thinking"})
	applyPrefillToResponse(r, "P")
	bs = r["content"].([]map[string]interface{})
	if len(bs) != 2 || bs[1]["text"] != "P" {
		t.Fatalf("append: %v", bs)
	}
	// Text block with non-string text still stops the search (no duplicate prefill).
	r = covBlocks(map[string]interface{}{"type": "text", "text": 5})
	applyPrefillToResponse(r, "P")
	if len(r["content"].([]map[string]interface{})) != 1 {
		t.Fatal("must not synthesize when a text block exists")
	}
}

func TestCov_StripOscIgnoreTag(t *testing.T) {
	r := covBlocks(covText("a [ignoring loop detection] b"), map[string]interface{}{"type": "tool_use"}, map[string]interface{}{"type": "text", "text": 3})
	stripOscIgnoreTag(r)
	if r["content"].([]map[string]interface{})[0]["text"] != "a  b" {
		t.Fatalf("%v", r)
	}
	stripOscIgnoreTag(map[string]interface{}{"content": "nope"}) // must not panic
	clean := covBlocks(covText("plain"))
	stripOscIgnoreTag(clean)
	if clean["content"].([]map[string]interface{})[0]["text"] != "plain" {
		t.Fatal("untagged text unchanged")
	}
}

func TestCov_AppendOrCoalesceText(t *testing.T) {
	r := map[string]interface{}{}
	appendOrCoalesceText(r, "")
	if _, ok := r["content"]; ok {
		t.Fatal("empty text is a no-op")
	}
	appendOrCoalesceText(r, "[ignoring loop detection]")
	if _, ok := r["content"]; ok {
		t.Fatal("tag-only text is a no-op")
	}
	appendOrCoalesceText(r, "Hi")
	appendOrCoalesceText(r, " there[ignoring loop detection]")
	bs := r["content"].([]map[string]interface{})
	if len(bs) != 1 || bs[0]["text"] != "Hi there" {
		t.Fatalf("coalesce: %v", bs)
	}
	r = covBlocks(map[string]interface{}{"type": "tool_use"})
	appendOrCoalesceText(r, "after")
	bs = r["content"].([]map[string]interface{})
	if len(bs) != 2 || bs[1]["text"] != "after" {
		t.Fatalf("new block after non-text: %v", bs)
	}
	r = map[string]interface{}{"content": []map[string]interface{}{}}
	appendOrCoalesceText(r, "x")
	if len(r["content"].([]map[string]interface{})) != 1 {
		t.Fatal("empty slice gets a block")
	}
}

func TestCov_CacheBreakpointPrefixFraction(t *testing.T) {
	if cacheBreakpointPrefixFraction(nil) != 0 || cacheBreakpointPrefixFraction([]byte(`{"a":1}`)) != 0 {
		t.Fatal("no breakpoint -> 0")
	}
	body := []byte(`{"cache_control":{}}` + string(make([]byte, 0)))
	if f := cacheBreakpointPrefixFraction(body); f != float64(1)/float64(len(body)) {
		t.Fatalf("frac %v", f)
	}
	body = []byte(`xxxxxxxx"cache_control"`)
	if f := cacheBreakpointPrefixFraction(body); f <= 0.3 || f >= 0.4 {
		t.Fatalf("offset proxy: %v", f)
	}
	if cacheCreationThreshold("claude-3-HAIKU") != 2048 || cacheCreationThreshold("opus") != 1024 {
		t.Fatal("threshold")
	}
}

func TestCov_StripThoughtFieldsForPublicAPI(t *testing.T) {
	// Non-Gemini / invalid bodies pass through byte-identical.
	for _, in := range []string{`{"x":1}`, `not json`} {
		if string(stripThoughtFieldsForPublicAPI([]byte(in))) != in {
			t.Fatalf("passthrough %q", in)
		}
	}
	// Valid contents without any thought fields: unchanged bytes.
	clean := `{"contents":[{"role":"user","parts":[{"text":"hi"}]},"junk",{"parts":"x"},{"parts":["s"]}]}`
	if string(stripThoughtFieldsForPublicAPI([]byte(clean))) != clean {
		t.Fatal("clean body must be returned untouched")
	}
	in := `{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}},"contents":[{"role":"model","parts":[
	 {"text":"t","thought":true,"thoughtSignature":"s"},
	 {"functionCall":{"name":"f"},"thoughtSignature":"s2"},
	 {"text":"keep"}]}]}`
	out := stripThoughtFieldsForPublicAPI([]byte(in))
	if gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Int() != 8192 {
		t.Fatalf("ModeAuto budget must be pinned: %s", out)
	}
	for _, p := range []string{"contents.0.parts.0.thought", "contents.0.parts.0.thoughtSignature", "contents.0.parts.1.thoughtSignature"} {
		if gjson.GetBytes(out, p).Exists() {
			t.Fatalf("%s must be stripped: %s", p, out)
		}
	}
	if gjson.GetBytes(out, "contents.0.parts.1.functionCall.name").String() != "f" || gjson.GetBytes(out, "contents.0.parts.2.text").String() != "keep" {
		t.Fatalf("payload content lost: %s", out)
	}
	// Budget-only change with non-array contents is still re-marshalled.
	only := `{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}},"contents":"x"}`
	out = stripThoughtFieldsForPublicAPI([]byte(only))
	if gjson.GetBytes(out, "generationConfig.thinkingConfig.thinkingBudget").Int() != 8192 {
		t.Fatalf("budget-only: %s", out)
	}
	// Positive budget untouched.
	pos := `{"generationConfig":{"thinkingConfig":{"thinkingBudget":100}},"contents":[]}`
	if string(stripThoughtFieldsForPublicAPI([]byte(pos))) != pos {
		t.Fatal("positive budget must be unchanged")
	}
	var m map[string]interface{}
	if json.Unmarshal(stripThoughtFieldsForPublicAPI([]byte(in)), &m) != nil {
		t.Fatal("output must be valid JSON")
	}
}

type covPlainWriter struct{ h http.Header }

func (p *covPlainWriter) Header() http.Header         { return p.h }
func (p *covPlainWriter) Write(b []byte) (int, error) { return len(b), nil }
func (p *covPlainWriter) WriteHeader(int)             {}

func TestCov_TrackCommit(t *testing.T) {
	rec := httptest.NewRecorder()
	w, c := trackCommit(rec)
	if c.Committed() {
		t.Fatal("fresh writer is uncommitted")
	}
	if _, ok := w.(http.Flusher); !ok {
		t.Fatal("flusher-ness must be preserved")
	}
	w.(http.Flusher).Flush()
	if c.Committed() {
		t.Fatal("Flush alone does not commit")
	}
	_, _ = w.Write([]byte("x"))
	if !c.Committed() {
		t.Fatal("Write commits")
	}
	// Re-wrapping an already tracked writer returns it as-is.
	w2, c2 := trackCommit(w)
	if !c2.Committed() || w2 != w {
		t.Fatal("must not double wrap")
	}
	// Unwrap exposes the underlying writer.
	ct := &commitTracker{ResponseWriter: rec}
	if ct.Unwrap() != http.ResponseWriter(rec) {
		t.Fatal("Unwrap")
	}
	ct.WriteHeader(204)
	if !ct.Committed() {
		t.Fatal("WriteHeader commits")
	}
	// Non-flusher stays a non-flusher.
	pw, pc := trackCommit(&covPlainWriter{h: http.Header{}})
	if _, ok := pw.(http.Flusher); ok {
		t.Fatal("non-flusher must not gain Flush")
	}
	pw.WriteHeader(200)
	if !pc.Committed() {
		t.Fatal("commit on plain writer")
	}
}
