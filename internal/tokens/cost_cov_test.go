package tokens

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claudgravity/internal/logx"
	"claudgravity/internal/protocol/claude"
)

func blockFrom(t *testing.T, raw string) claude.Block {
	t.Helper()
	var b claude.Block
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBlockCostP_ToolAndSearchBlocks(t *testing.T) {
	tu := blockFrom(t, `{"type":"tool_use","id":"a","name":"Read","input":{"p":"x"}}`)
	if got, want := blockCostP(tu, false, FlatTextTokens, Gemini2Profile), FlatTextTokens("Read")+FlatTextTokens(`{"p":"x"}`); got != want {
		t.Errorf("tool_use = %d want %d", got, want)
	}
	su := blockFrom(t, `{"type":"server_tool_use","id":"a","name":"web_search","input":{"query":"q"}}`)
	if got, want := blockCostP(su, false, FlatTextTokens, Gemini2Profile), FlatTextTokens("web_search")+FlatTextTokens(`{"query":"q"}`); got != want {
		t.Errorf("server_tool_use = %d want %d", got, want)
	}
	ws := blockFrom(t, `{"type":"web_search_tool_result","tool_use_id":"a","content":[
		{"type":"web_search_result","title":"T1","url":"https://a","snippet":"S1"},
		{"type":"web_search_result","title":"T2","url":"https://b"}]}`)
	want := FlatTextTokens("T1") + FlatTextTokens("https://a") + FlatTextTokens("S1") +
		FlatTextTokens("T2") + FlatTextTokens("https://b") + FlatTextTokens("")
	if got := blockCostP(ws, false, FlatTextTokens, Gemini2Profile); got != want {
		t.Errorf("web_search_tool_result = %d want %d", got, want)
	}
}

func TestBlockCostP_ToolResultContentShapes(t *testing.T) {
	cases := []struct {
		name, content string
		want          int
	}{
		{"string", `"hello"`, FlatTextTokens("hello")},
		{"typed blocks", `[{"type":"text","text":"abc"},{"type":"text","text":"de"}]`, FlatTextTokens("abc") + FlatTextTokens("de")},
		{"bare strings", `["a","bb"]`, FlatTextTokens("a") + FlatTextTokens("bb")},
		{"bare object", `{"text":"hi"}`, FlatTextTokens(`{"text":"hi"}`)},
		{"empty array", `[]`, 0},
	}
	for _, c := range cases {
		b := blockFrom(t, `{"type":"tool_result","tool_use_id":"t","content":`+c.content+`}`)
		if got := blockCostP(b, false, FlatTextTokens, Gemini2Profile); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
	if got := toolResultCostP(nil, false, FlatTextTokens, Gemini2Profile); got != 0 {
		t.Errorf("empty content = %d", got)
	}
}

func TestImageCostP_URLAndSVG(t *testing.T) {
	url := claude.ImageSource{Type: "url", URL: "https://x/y.png"}
	if got, want := imageCostP(url, FlatTextTokens, Gemini2Profile), FlatTextTokens("[Attached by URL: https://x/y.png]"); got != want {
		t.Errorf("url = %d want %d", got, want)
	}
	if got, want := imageCostP(claude.ImageSource{Type: "url"}, FlatTextTokens, Gemini2Profile), FlatTextTokens("[Attached by URL: ]"); got != want {
		t.Errorf("empty url = %d want %d", got, want)
	}
	data := strings.Repeat("A", 400) // 300 decoded bytes -> 150 tokens
	svg := claude.ImageSource{Type: "base64", MediaType: "IMAGE/SVG+XML; charset=utf-8", Data: data}
	if got := imageCostP(svg, FlatTextTokens, Gemini2Profile); got != 150 {
		t.Errorf("svg = %d want 150", got)
	}
	tiny := claude.ImageSource{Type: "base64", MediaType: "image/svg+xml", Data: "AA"}
	if got := imageCostP(tiny, FlatTextTokens, Gemini2Profile); got != 1 {
		t.Errorf("tiny svg = %d want 1", got)
	}
	png := claude.ImageSource{Type: "base64", MediaType: "image/png", Data: pngB64(t, 100, 100)}
	if got := imageCostP(png, FlatTextTokens, Gemini2Profile); got != 258 {
		t.Errorf("png = %d want 258", got)
	}
}

func TestDocumentCostP_Sources(t *testing.T) {
	flat := FlatTextTokens
	if got := documentCostP(claude.DocumentSource{Type: "url", URL: "https://d"}, flat, Gemini2Profile); got != flat("[Attached by URL: https://d]") {
		t.Errorf("url doc = %d", got)
	}
	if got := documentCostP(claude.DocumentSource{Type: "url"}, flat, Gemini2Profile); got != flat("[Attached by URL: ]") {
		t.Errorf("empty url doc = %d", got)
	}
	if got := documentCostP(claude.DocumentSource{Type: "text", Data: "plain"}, flat, Gemini2Profile); got != flat("plain") {
		t.Errorf("default doc = %d", got)
	}
	// Opaque (non-PDF) base64 payload with no page structure: density rule, floored.
	pdf := claude.DocumentSource{Type: "base64", MediaType: "application/pdf", Data: strings.Repeat("A", 1000)}
	if got := documentCostP(pdf, flat, Gemini2Profile); got != minPDFTokenFloor {
		t.Errorf("small pdf should be floored: %d", got)
	}
	big := claude.DocumentSource{Type: "base64", Data: strings.Repeat("A", 5_000_000)}
	if got := documentCostP(big, flat, Gemini2Profile); got != 5_000_000/500 {
		t.Errorf("big pdf (default media type) = %d", got)
	}
	img := claude.DocumentSource{Type: "base64", MediaType: "image/png", Data: pngB64(t, 100, 100)}
	if got := documentCostP(img, flat, Gemini2Profile); got != 258 {
		t.Errorf("image doc = %d", got)
	}
	txt := claude.DocumentSource{Type: "base64", MediaType: "text/csv", Data: strings.Repeat("A", 400)}
	if got := documentCostP(txt, flat, Gemini2Profile); got != 150 {
		t.Errorf("text doc = %d", got)
	}
	tiny := claude.DocumentSource{Type: "base64", MediaType: "text/csv", Data: "A"}
	if got := documentCostP(tiny, flat, Gemini2Profile); got != 1 {
		t.Errorf("tiny text doc = %d", got)
	}
	// Document title and context are priced in addition to the source.
	b := blockFrom(t, `{"type":"document","title":"Title","context":"Ctx","source":{"type":"text","data":"body"}}`)
	if got, want := blockCostP(b, false, flat, Gemini2Profile), flat("body")+flat("Title")+flat("Ctx"); got != want {
		t.Errorf("document block = %d want %d", got, want)
	}
}

func TestDocumentContentText(t *testing.T) {
	cases := map[string]string{
		`"literal"`:                                     "literal",
		`["a",{"type":"text","text":"b"},"c"]`:          "abc",
		`[{"type":"image","text":"skip"},{"text":"x"}]`: "",
		`{"not":"array"}`:                               "",
		``:                                              "",
	}
	for in, want := range cases {
		if got := documentContentText(json.RawMessage(in)); got != want {
			t.Errorf("documentContentText(%s) = %q want %q", in, got, want)
		}
	}
	doc := claude.DocumentSource{Type: "content", Content: json.RawMessage(`["ab","cd"]`)}
	if got := documentCostP(doc, FlatTextTokens, Gemini2Profile); got != FlatTextTokens("abcd") {
		t.Errorf("content doc = %d", got)
	}
}

func TestFlatJSONTokensAndContextBudget(t *testing.T) {
	if FlatJSONTokens(nil) != 1 || FlatJSONTokens([]byte("a")) != 1 || FlatJSONTokens([]byte("abcd")) != 2 {
		t.Error("FlatJSONTokens: floor of 1, then len/2")
	}
	if got := ContextBudget(100_000, 10_000); got != 100_000-10_000-ContextSafetyMarginTokens {
		t.Errorf("normal budget = %d", got)
	}
	if got := ContextBudget(10_000, 9_000); got != 5_000 {
		t.Errorf("exhausted budget should fall back to half the window: %d", got)
	}
	if got := ContextBudget(10_000, 8_000); got != 5_000 {
		t.Errorf("budget == 0 should fall back too: %d", got)
	}
}

func TestConservativeToolsTokens(t *testing.T) {
	if ConservativeToolsTokens(nil) != 0 || ConservativeSystemTokens(nil) != 0 {
		t.Error("empty inputs must price at 0")
	}
	stub := []claude.Tool{{Type: "bash_20250124", Name: "bash"}}
	custom := []claude.Tool{{Type: "mystery_tool_1", Name: "bash"}}
	withSchema := ConservativeToolsTokens(stub)
	if withSchema <= ConservativeToolsTokens(custom) {
		t.Errorf("builtin tool must be priced with its declared schema: %d vs %d", withSchema, ConservativeToolsTokens(custom))
	}
	if stub[0].InputSchema != nil {
		t.Error("input slice must not be mutated")
	}
	declared := []claude.Tool{{Name: "x", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	if ConservativeToolsTokens(declared) < 1 {
		t.Error("declared tool must price > 0")
	}
}

func TestLogDriftRatio(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drift.log")
	logx.InitLogger(path)
	LogDriftRatio(7, 100, 0)
	LogDriftRatio(7, 100, -5)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("non-positive upstream count must not log")
	}
	LogDriftRatio(42, 130, 100)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "[driftRatio] rid=42 ratio=1.3000") {
		t.Errorf("log line: %q", data)
	}
	if strings.Contains(string(data), "130") && !strings.Contains(string(data), "1.3000") {
		t.Error("raw counts must not be logged")
	}
}
