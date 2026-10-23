package translate

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
)

func convertMsgs(t *testing.T, role, content string) []string {
	t.Helper()
	body := `{"messages":[{"role":"` + role + `","content":` + content + `}]}`
	root := gjson.Parse(body)
	out := translateMessages(`{"contents":[]}`, root, toolNameInfo{}, false, "s")
	var texts []string
	gjson.Get(out, "contents.0.parts").ForEach(func(_, p gjson.Result) bool {
		texts = append(texts, p.Get("text").String())
		return true
	})
	return texts
}

func TestUnknownBlocksBecomeLabelledText(t *testing.T) {
	cases := []struct {
		name, block string
		want        []string
	}{
		{"search_result", `{"type":"search_result","title":"Doc A","source":"https://a.example","content":[{"type":"text","text":"alpha body"}]}`, []string{"Doc A", "https://a.example", "alpha body"}},
		{"mcp_tool_use", `{"type":"mcp_tool_use","id":"m1","name":"lookup","server_name":"srv","input":{"q":"x"}}`, []string{"srv.lookup", `"q":"x"`}},
		{"mcp_tool_result", `{"type":"mcp_tool_result","tool_use_id":"m1","content":[{"type":"text","text":"mcp output"}]}`, []string{"MCP tool result", "mcp output"}},
		{"code_execution_tool_result", `{"type":"code_execution_tool_result","tool_use_id":"c1","content":{"type":"code_execution_result","stdout":"hello out","stderr":"","return_code":0}}`, []string{"hello out", "return_code: 0"}},
		{"web_search_tool_result", `{"type":"web_search_tool_result","tool_use_id":"w1","content":[{"type":"web_search_result","title":"T","url":"https://t.example","snippet":"snip"}]}`, []string{"T", "https://t.example", "snip"}},
		{"server_tool_use", `{"type":"server_tool_use","id":"s1","name":"web_search","input":{"query":"q"}}`, []string{"web_search", "query"}},
		{"compaction", `{"type":"compaction","content":"earlier we discussed X"}`, []string{"earlier we discussed X"}},
		{"tool_reference", `{"type":"tool_reference","tool_name":"Grep"}`, []string{"[tool: Grep]"}},
	}
	for _, c := range cases {
		texts := convertMsgs(t, "user", `[`+c.block+`]`)
		joined := strings.Join(texts, "\n")
		if joined == "..." || joined == "" {
			t.Fatalf("%s collapsed to placeholder", c.name)
		}
		for _, w := range c.want {
			if !strings.Contains(joined, w) {
				t.Errorf("%s: missing %q in %q", c.name, w, joined)
			}
		}
	}
}

func TestUnknownBlockTruncated(t *testing.T) {
	big := strings.Repeat("z", 50000)
	texts := convertMsgs(t, "user", `[{"type":"mcp_tool_result","content":"`+big+`"}]`)
	if len(texts[0]) > blockTextMaxBytes+100 || !strings.Contains(texts[0], "[truncated]") {
		t.Fatalf("expected truncation, len=%d", len(texts[0]))
	}
}

func TestEmptyMessageKeepsMinimalPartOnlyWhenNothingElse(t *testing.T) {
	for _, c := range []string{`""`, `[]`, `[{"type":"text","text":""}]`} {
		if texts := convertMsgs(t, "user", c); len(texts) != 1 || texts[0] != "..." {
			t.Errorf("%s: got %v", c, texts)
		}
	}
	if texts := convertMsgs(t, "user", `[{"type":"text","text":""},{"type":"tool_reference","tool_name":"X"}]`); len(texts) != 1 || texts[0] != "[tool: X]" {
		t.Errorf("placeholder must not be added beside real parts: %v", texts)
	}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func attach(t *testing.T, ptype, mt, data string) string {
	t.Helper()
	blk := `{"type":"` + ptype + `","source":{"type":"base64","media_type":"` + mt + `","data":"` + data + `"}}`
	parts := convertImageDocumentBlock(ptype, gjson.Parse(blk), "user")
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %v", parts)
	}
	return parts[0]
}

func TestAttachmentMIMEPolicy(t *testing.T) {
	gif := b64("GIF89a\x01\x00\x01\x00")
	if p := attach(t, "image", "image/gif", gif); gjson.Get(p, "inlineData").Exists() || !strings.Contains(gjson.Get(p, "text").String(), "[image omitted: unsupported type image/gif]") {
		t.Fatalf("gif must be a placeholder: %s", p)
	}
	if p := attach(t, "image", "image/png", tinyPNGB64); gjson.Get(p, "inlineData.mimeType").String() != "image/png" {
		t.Fatalf("png must be inlineData: %s", p)
	}
	jpeg := b64("\xff\xd8\xff\xe0\x00\x10JFIF\x00")
	if p := attach(t, "image", "image/png", jpeg); gjson.Get(p, "inlineData.mimeType").String() != "image/jpeg" {
		t.Fatalf("mislabelled jpeg must be sent with the sniffed type: %s", p)
	}
	if p := attach(t, "image", "image/png", b64("this is not an image")); gjson.Get(p, "inlineData").Exists() {
		t.Fatalf("non-image bytes must not be forwarded: %s", p)
	}
	if p := attach(t, "document", "application/pdf", tinyPDFB64); gjson.Get(p, "inlineData.mimeType").String() != "application/pdf" {
		t.Fatalf("pdf must be inlineData: %s", p)
	}
	if p := attach(t, "document", "application/pdf", b64("not a pdf")); gjson.Get(p, "inlineData").Exists() {
		t.Fatalf("fake pdf must not be forwarded: %s", p)
	}
	for _, mt := range []string{
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/zip", "application/octet-stream",
	} {
		p := attach(t, "document", mt, b64("PK\x03\x04\x00\x00binary\xff\xfe"))
		txt := gjson.Get(p, "text").String()
		if gjson.Get(p, "inlineData").Exists() || !strings.Contains(txt, "[document omitted: unsupported type "+mt) || strings.ContainsRune(txt, '�') {
			t.Fatalf("%s must be a clean placeholder: %q", mt, txt)
		}
	}
	if p := attach(t, "document", "text/csv", b64("a,b\n1,2\n")); gjson.Get(p, "text").String() != "a,b\n1,2\n" {
		t.Fatalf("csv must be decoded text: %s", p)
	}
	if p := attach(t, "document", "application/json", b64(`{"k":1}`)); gjson.Get(p, "text").String() != `{"k":1}` {
		t.Fatalf("json must be decoded text: %s", p)
	}
	if p := attach(t, "document", "text/plain", b64("bad\xff\xfeutf8")); !strings.Contains(gjson.Get(p, "text").String(), "omitted") {
		t.Fatalf("invalid utf-8 text must be a placeholder: %s", p)
	}
	for _, mt := range []string{"text/plain", "image/png", "application/pdf"} {
		if p := attach(t, "document", mt, "@@not*base64!!"); !strings.Contains(gjson.Get(p, "text").String(), "not valid base64") {
			t.Fatalf("%s: invalid base64 must be a placeholder: %s", mt, p)
		}
	}
}

func TestResolveFetchedMIME(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString(tinyPNGB64)
	cases := []struct {
		ct, url string
		body    []byte
		want    string
	}{
		{"image/png", "https://x/a", png, "image/png"},
		{"application/octet-stream", "https://x/a", png, "image/png"},
		{"", "https://x/a", png, "image/png"},
		{"binary/octet-stream", "https://x/a.bin", []byte("%PDF-1.4"), "application/pdf"},
		{"application/octet-stream", "https://x/photo.heic?sig=1", []byte("\x00\x00\x00\x18ftypheic\x00"), "image/heic"},
		{"application/octet-stream", "https://x/report.docx", []byte("\x00\x01\x02\x03\x04"), "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"", "https://x/readme.md", []byte("\x00\x01\x02"), "text/markdown"},
		{"text/csv; charset=utf-8", "https://x/a", []byte("a,b"), "text/csv"},
	}
	for _, c := range cases {
		if got := resolveFetchedMIME(c.ct, c.url, c.body); got != c.want {
			t.Errorf("ct=%q url=%s: got %q want %q", c.ct, c.url, got, c.want)
		}
	}
}

func TestToolResultNestedTypedItemsAndImageSameTurn(t *testing.T) {
	SetAllowHistoryTruncation(false)
	body := `{"model":"claude-sonnet-4-5","max_tokens":1024,"tools":[{"name":"Shot","input_schema":{"type":"object","properties":{}}}],"messages":[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Shot","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[` +
		`{"type":"tool_reference","tool_name":"Grep"},` +
		`{"type":"search_result","title":"Nested","source":"https://n.example","content":[{"type":"text","text":"nested body"}]},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + tinyPNGB64 + `"}}]}]}]}`
	res, err := ConvertClaudeRequestToGemini([]byte(body), config.TenantConfig{DefaultModel: "gemini-3.8-flash"}, "", "main", "s9", "", true)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(res.GeminiBody)
	if strings.Contains(raw, "Tool output received") || strings.Contains(raw, "Visual output from") {
		t.Fatalf("fabricated model turn must be gone: %s", raw)
	}
	contents := gjson.GetBytes(res.GeminiBody, "contents").Array()
	last := contents[len(contents)-1]
	if last.Get("role").String() != "user" {
		t.Fatalf("last turn must be user: %s", last.Raw)
	}
	parts := last.Get("parts").Array()
	if len(parts) != 2 || !parts[0].Get("functionResponse").Exists() || parts[1].Get("inlineData.mimeType").String() != "image/png" {
		t.Fatalf("expected functionResponse then image in one turn: %s", last.Raw)
	}
	res0 := parts[0].Get("functionResponse.response").Raw
	if !strings.Contains(res0, "[tool: Grep]") || !strings.Contains(res0, "nested body") {
		t.Fatalf("nested typed items must be rendered: %s", res0)
	}
	for i := 1; i < len(contents); i++ {
		if contents[i].Get("role").String() == contents[i-1].Get("role").String() {
			t.Fatalf("roles must alternate: %s", raw)
		}
	}
}
