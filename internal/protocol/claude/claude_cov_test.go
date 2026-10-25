package claude

import (
	"encoding/json"
	"testing"
)

func TestCovBlockAccessors(t *testing.T) {
	mk := func(s string) Block {
		var b Block
		if err := json.Unmarshal([]byte(s), &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	th := mk(`{"type":"thinking","thinking":"hm","signature":"sig"}`)
	if v, ok := th.AsThinking(); !ok || v.Thinking != "hm" || v.Signature != "sig" {
		t.Fatalf("thinking %+v %v", v, ok)
	}
	if _, ok := mk(`{"type":"text","text":"x"}`).AsThinking(); ok {
		t.Fatal("text must not decode as thinking")
	}
	if v, ok := mk(`{"type":"redacted_thinking","data":"blob"}`).AsRedactedThinking(); !ok || v.Data != "blob" {
		t.Fatalf("redacted %+v", v)
	}
	if _, ok := th.AsRedactedThinking(); ok {
		t.Fatal("mismatch must be false")
	}
	img := mk(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}}`)
	if v, ok := img.AsImage(); !ok || v.Source.MediaType != "image/png" || v.Source.Data != "AA" {
		t.Fatalf("image %+v", v)
	}
	if _, ok := th.AsImage(); ok {
		t.Fatal("mismatch must be false")
	}
	doc := mk(`{"type":"document","title":"T","source":{"type":"url","url":"http://x"}}`)
	if v, ok := doc.AsDocument(); !ok || v.Title != "T" || v.Source.URL != "http://x" {
		t.Fatalf("doc %+v", v)
	}
	if _, ok := img.AsDocument(); ok {
		t.Fatal("mismatch must be false")
	}
	stu := mk(`{"type":"server_tool_use","id":"s1","name":"web_search","input":{"q":"a"}}`)
	if v, ok := stu.AsServerToolUse(); !ok || v.ID != "s1" || string(v.Input) != `{"q":"a"}` {
		t.Fatalf("stu %+v", v)
	}
	if _, ok := th.AsServerToolUse(); ok {
		t.Fatal("mismatch must be false")
	}
	wr := mk(`{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","title":"A","url":"u"}]}`)
	if v, ok := wr.AsWebSearchToolResult(); !ok || len(v.Content) != 1 || v.Content[0].Title != "A" {
		t.Fatalf("wsr %+v", v)
	}
	if _, ok := th.AsWebSearchToolResult(); ok {
		t.Fatal("mismatch must be false")
	}
}

func TestCovBlockMarshalNilRaw(t *testing.T) {
	out, err := json.Marshal(Block{})
	if err != nil || string(out) != "null" {
		t.Fatalf("got %s %v", out, err)
	}
	out, err = json.Marshal(Block{Type: "x", Raw: json.RawMessage(`{"type":"x","k":1}`)})
	if err != nil || string(out) != `{"type":"x","k":1}` {
		t.Fatalf("got %s %v", out, err)
	}
}

func TestCovSystemText(t *testing.T) {
	cases := []struct {
		name, raw, want string
	}{
		{"empty", ``, ""},
		{"string", `"hello"`, "hello"},
		{"blocks", `[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "a\nb\n"},
		{"strings", `["a","b"]`, "a\nb"},
		{"garbage", `42`, ""},
	}
	for _, c := range cases {
		r := Request{System: SystemPrompt(c.raw)}
		if got := r.SystemText(); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}
