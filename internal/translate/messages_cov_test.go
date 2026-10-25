package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func covTranslate(body string, tn toolNameInfo, antigravity bool) string {
	root := gjson.Parse(body)
	return translateMessages(`{"contents":[]}`, root, tn, antigravity, "cov-sess")
}

func covTN(names ...string) toolNameInfo {
	tn := toolNameInfo{
		declaredToolNames:     map[string]bool{},
		effectiveToolNames:    map[string]bool{},
		disambiguatedToolName: map[string]string{},
	}
	for _, n := range names {
		tn.declaredToolNames[n] = true
		tn.effectiveToolNames[n] = true
	}
	return tn
}

func TestCovWebSearchToolResultFormats(t *testing.T) {
	cases := []struct {
		name, block, want string
	}{
		{"results", `{"type":"web_search_tool_result","content":[{"type":"web_search_result","title":"A","url":"http://a","snippet":"sn"}]}`,
			"[Web Search Results]:\n- A (http://a): sn"},
		{"mixed items", `{"type":"web_search_tool_result","content":["plain",{"type":"other","x":1}]}`,
			"[Web Search Results]:\nplain\n" + `{"type":"other","x":1}`},
		{"string content", `{"type":"web_search_tool_result","content":"raw text"}`, "[Web Search Results]:\nraw text"},
		{"empty array", `{"type":"web_search_tool_result","content":[]}`, "[Web Search Results: none]"},
		{"missing content", `{"type":"web_search_tool_result"}`, "[Web Search Results: none]"},
	}
	for _, c := range cases {
		parts := convertWebSearchToolResultBlock(gjson.Parse(c.block))
		if len(parts) != 1 || gjson.Get(parts[0], "text").String() != c.want {
			t.Errorf("%s: got %v want %q", c.name, parts, c.want)
		}
	}
}

func TestCovTranslateMessagesNonArrayMessagesIsNoop(t *testing.T) {
	if got := covTranslate(`{"messages":"nope"}`, toolNameInfo{}, false); got != `{"contents":[]}` {
		t.Fatalf("got %s", got)
	}
}

func TestCovTranslateMessagesMergesSameRoleAndPlaceholders(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":"first"},
		{"role":"user","content":[{"type":"text","text":"second"}]},
		{"role":"assistant","content":[]},
		{"role":"user","content":"   "}]}`
	out := covTranslate(body, toolNameInfo{}, false)
	if gjson.Get(out, "contents.#").Int() != 3 {
		t.Fatalf("same-role turns must merge into one content: %s", out)
	}
	if gjson.Get(out, "contents.0.parts.#").Int() != 2 || gjson.Get(out, "contents.0.parts.1.text").String() != "second" {
		t.Fatalf("merge wrong: %s", out)
	}
	if gjson.Get(out, "contents.1.role").String() != "model" || gjson.Get(out, "contents.1.parts.0.text").String() != "..." {
		t.Fatalf("empty assistant turn needs a placeholder: %s", out)
	}
	if gjson.Get(out, "contents.1.parts.0.thoughtSignature").Exists() {
		t.Fatalf("non-antigravity placeholder must not carry a signature: %s", out)
	}
	if gjson.Get(out, "contents.2.parts.0.text").String() != "..." {
		t.Fatalf("blank user turn placeholder: %s", out)
	}
}

func TestCovTranslateMessagesAntigravityPlaceholderSignature(t *testing.T) {
	out := covTranslate(`{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":""}]}`, toolNameInfo{}, true)
	if gjson.Get(out, "contents.1.parts.0.thoughtSignature").String() != "skip_thought_signature_validator" {
		t.Fatalf("got %s", out)
	}
}

func TestCovTranslateMessagesToolRoundTrip(t *testing.T) {
	body := `{"messages":[
		{"role":"user","content":"go"},
		{"role":"assistant","content":[
			{"type":"text","text":"calling"},
			{"type":"tool_use","id":"tu1","name":"Read","input":{"file_path":"a"}},
			{"type":"server_tool_use","name":"web_search","input":{"q":"z"}}]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"tu1","content":"file body"},
			{"type":"text","text":"and then"}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":""}]}]}`
	out := covTranslate(body, covTN("Read"), false)
	if gjson.Get(out, "contents.1.role").String() != "model" {
		t.Fatalf("%s", out)
	}
	if gjson.Get(out, "contents.1.parts.1.functionCall.name").String() != "Read" {
		t.Fatalf("functionCall missing: %s", out)
	}
	if !strings.Contains(gjson.Get(out, "contents.1.parts.2.text").String(), "[Executed server tool web_search") {
		t.Fatalf("server tool use must degrade to text: %s", out)
	}
	// functionResponse parts are sealed apart from plain text parts.
	var fr, errResp int
	gjson.Get(out, "contents").ForEach(func(_, c gjson.Result) bool {
		c.Get("parts").ForEach(func(_, p gjson.Result) bool {
			if p.Get("functionResponse").Exists() {
				fr++
				if p.Get("functionResponse.response.error").Exists() {
					errResp++
				}
			}
			return true
		})
		return true
	})
	if fr != 2 || errResp != 1 {
		t.Fatalf("functionResponses=%d errors=%d: %s", fr, errResp, out)
	}
	if !strings.Contains(out, "Tool execution failed (error flag set, no output).") {
		t.Fatalf("empty error result needs explicit text: %s", out)
	}
}

func TestCovTranslateMessagesThoughtPartsReorderedFirst(t *testing.T) {
	body := `{"messages":[
		{"role":"assistant","content":[{"type":"text","text":"answer"}]},
		{"role":"assistant","content":[{"type":"thinking","thinking":"deep","signature":"sig"}]}]}`
	out := covTranslate(body, toolNameInfo{}, true)
	if gjson.Get(out, "contents.#").Int() != 1 {
		t.Fatalf("assistant turns must merge: %s", out)
	}
	first := gjson.Get(out, "contents.0.parts.0")
	if !first.Get("thought").Bool() {
		t.Fatalf("thought part must lead the merged model turn: %s", out)
	}
}
