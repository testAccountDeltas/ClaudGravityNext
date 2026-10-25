package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCovBlockToText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"bare string", `"hello"`, "hello"},
		{"bare number", `42`, "42"},
		{"bare array", `[1,2]`, "[1,2]"},
		{"text", `{"type":"text","text":"  hi  "}`, "hi"},
		{"tool_reference", `{"type":"tool_reference","tool_name":"Read"}`, "[tool: Read]"},
		{"search_result full", `{"type":"search_result","title":"T","source":"http://s","content":[{"type":"text","text":"body"}]}`,
			"[Search result: T]\nSource: http://s\nbody"},
		{"search_result bare", `{"type":"search_result"}`, "[Search result]"},
		{"compaction", `{"type":"compaction","content":"summary"}`, "[Conversation summary]\nsummary"},
		{"mcp_tool_use with server and input", `{"type":"mcp_tool_use","server_name":"srv","name":"do","input":{"a":1}}`,
			`[MCP tool call srv.do input: {"a":1}]`},
		{"mcp_tool_use no server empty input", `{"type":"mcp_tool_use","name":"do","input":{}}`, "[MCP tool call do]"},
		{"mcp_tool_result ok", `{"type":"mcp_tool_result","content":"fine"}`, "[MCP tool result]\nfine"},
		{"mcp_tool_result error", `{"type":"mcp_tool_result","is_error":true,"content":[{"type":"text","text":"bad"}]}`,
			"[MCP tool result (error)]\nbad"},
		{"web_search_tool_result", `{"type":"web_search_tool_result","content":[{"type":"web_search_result","title":"A","url":"u","snippet":"s"},{"type":"web_search_result","title":"B","url":"v"}]}`,
			"[Web search results]\n- A (u): s\n- B (v)"},
		{"server_tool_use input", `{"type":"server_tool_use","name":"web_search","input":{"q":"x"}}`,
			`[Executed server tool web_search with input: {"q":"x"}]`},
		{"server_tool_use no input", `{"type":"server_tool_use","name":"web_search"}`, "[Executed server tool web_search]"},
		{"code_execution_tool_result", `{"type":"code_execution_tool_result","content":{"stdout":"out","stderr":"err","return_code":0}}`,
			"[code execution result]\nstdout:\nout\nstderr:\nerr\nreturn_code: 0"},
		{"bash_code_execution_result", `{"type":"bash_code_execution_result","stdout":"o"}`, "stdout:\no"},
		{"code_execution_result empty", `{"type":"code_execution_result"}`, `{"type":"code_execution_result"}`},
		{"unknown typed with content", `{"type":"widget","content":"inner"}`, "[widget] inner"},
		{"unknown typed no content", `{"type":"widget","x":1}`, `[widget] {"type":"widget","x":1}`},
		{"untyped object", `{"x":1}`, `{"x":1}`},
	}
	for _, c := range cases {
		if got := blockToText(gjson.Parse(c.in)); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestCovBlockToTextTruncatesRuneSafely(t *testing.T) {
	// 3-byte runes straddle the 16000-byte boundary.
	big := strings.Repeat("\u20ac", 6000)
	got := blockToText(gjson.Parse(`{"type":"text","text":"` + big + `"}`))
	if !strings.HasSuffix(got, "\n... [truncated]") {
		t.Fatalf("missing truncation marker")
	}
	body := strings.TrimSuffix(got, "\n... [truncated]")
	if len(body) > blockTextMaxBytes || strings.ContainsRune(body, '\uFFFD') {
		t.Fatalf("bad cut: len=%d", len(body))
	}
	if body != strings.Repeat("\u20ac", len(body)/3) {
		t.Fatal("cut must land on a rune boundary")
	}
}

func TestCovContentToText(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"null", `null`, ""},
		{"string", `"plain"`, "plain"},
		{"array skips empties", `[{"type":"text","text":"a"},{"type":"text","text":""},"b"]`, "a\nb"},
		{"typed object", `{"type":"tool_reference","tool_name":"X"}`, "[tool: X]"},
		{"exec object", `{"stdout":"o","stderr":"e"}`, "stdout:\no\nstderr:\ne\n"},
		{"stderr only", `{"stderr":"e"}`, "stderr:\ne\n"},
		{"plain object", `{"k":"v"}`, `{"k":"v"}`},
		{"number", `7`, "7"},
		{"bool", `true`, "true"},
	}
	for _, c := range cases {
		if got := contentToText(gjson.Parse(c.in)); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	if contentToText(gjson.Result{}) != "" {
		t.Error("missing content must be empty")
	}
}
