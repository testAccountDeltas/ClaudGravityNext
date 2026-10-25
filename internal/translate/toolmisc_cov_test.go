package translate

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts"
	"claudgravity/internal/loopguard"
	"claudgravity/internal/protocol/claude"
)

func TestCovResolveToolName(t *testing.T) {
	cases := []struct {
		in, name, typ string
	}{
		{`{"name":"Read","type":"custom"}`, "Read", "custom"},
		{`{"type":"text_editor_20250728"}`, "str_replace_editor", "text_editor_20250728"},
		{`{"type":"text_editor"}`, "str_replace_editor", "text_editor"},
		{`{"type":"bash_20250124"}`, "bash", "bash_20250124"},
		{`{"type":"bash"}`, "bash", "bash"},
		{`{"type":"web_search_20250305"}`, "web_search", "web_search_20250305"},
		{`{"type":"computer_20250124"}`, "computer", "computer_20250124"},
		{`{"type":"20250101"}`, "20250101", "20250101"},
		{`{}`, "", ""},
	}
	for _, c := range cases {
		n, ty := resolveToolName(gjson.Parse(c.in))
		if n != c.name || ty != c.typ {
			t.Errorf("%s: got (%q,%q) want (%q,%q)", c.in, n, ty, c.name, c.typ)
		}
	}
}

func TestCovIsUnsupportedServerTool(t *testing.T) {
	cases := []struct {
		name, tool string
		want       bool
	}{
		{"untyped", `{"name":"x"}`, false},
		{"custom", `{"type":"custom","name":"x"}`, false},
		{"has schema", `{"type":"advisor_1","name":"advisor","input_schema":{"type":"object"}}`, false},
		{"empty schema is no schema", `{"type":"advisor_1","name":"advisor","input_schema":{}}`, true},
		{"null schema", `{"type":"advisor_1","name":"advisor","input_schema":null}`, true},
		{"unknown server tool", `{"type":"tool_search_20260101","name":"tool_search"}`, true},
		{"web search is served natively", `{"type":"web_search_20250305","name":"web_search"}`, false},
		{"builtin by type", `{"type":"bash_20250124","name":"anything"}`, false},
		{"builtin by name", `{"type":"zzz_1","name":"bash"}`, false},
	}
	for _, c := range cases {
		tool := gjson.Parse(c.tool)
		if got := isUnsupportedServerTool(tool, tool.Get("name").String()); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestCovConvertToolResultBlockPaths(t *testing.T) {
	tn := covTN("Read")
	run := func(block, role string, ids map[string]string, tn toolNameInfo) []string {
		return convertToolResultBlock(gjson.Parse(block), role, ids, tn, gjson.Result{})
	}
	ids := map[string]string{"t1": "Read"}

	// Wrong role degrades to a note.
	if got := run(`{"type":"tool_result","tool_use_id":"t1","content":"x"}`, "assistant", ids, tn); len(got) != 1 ||
		gjson.Get(got[0], "text").String() != "[tool_result in unexpected role]" {
		t.Fatalf("got %v", got)
	}

	// Orphan result with mixed array content becomes plain text.
	orphan := `{"type":"tool_result","tool_use_id":"gone","content":["s",{"type":"text","text":"t"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}},{"type":"tool_reference","tool_name":"R"}]}`
	got := run(orphan, "user", ids, tn)
	txt := gjson.Get(got[0], "text").String()
	if !strings.HasPrefix(txt, "[Prior tool result for gone omitted]: st[binary attachment]") || !strings.Contains(txt, "[tool: R]") {
		t.Fatalf("orphan text = %q", txt)
	}

	// Long orphan content is truncated.
	long := `{"type":"tool_result","tool_use_id":"gone","content":"` + strings.Repeat("x", 800) + `"}`
	got = run(long, "user", ids, tn)
	if txt := gjson.Get(got[0], "text").String(); !strings.HasSuffix(txt, "... [truncated]") || len(txt) > 600 {
		t.Fatalf("not truncated: len=%d", len(txt))
	}

	// Result for a tool no longer declared this turn is summarized.
	got = run(`{"type":"tool_result","tool_use_id":"t1","content":"`+strings.Repeat("y", 300)+`"}`, "user", ids, covTN("Other"))
	txt = gjson.Get(got[0], "text").String()
	if !strings.Contains(txt, "earlier call to the Read tool") || !strings.HasSuffix(txt, "is omitted here because that tool is not available in this turn.)") {
		t.Fatalf("undeclared text = %q", txt)
	}

	// Array content: strings, numbers, untyped objects, nested typed blocks.
	arr := `{"type":"tool_result","tool_use_id":"t1","content":["a",5,true,{"k":"v"},{"type":"tool_reference","tool_name":"Z"},{"type":"search_result","title":"S"}]}`
	got = run(arr, "user", ids, tn)
	res := gjson.Get(got[0], "functionResponse.response.result").String()
	if res != `a5true{"k":"v"}`+"\n[tool: Z]\n[Search result: S]" {
		t.Fatalf("array flatten = %q", res)
	}
	if gjson.Get(got[0], "functionResponse.id").String() != "t1" || gjson.Get(got[0], "functionResponse.name").String() != "Read" {
		t.Fatalf("id/name wrong: %s", got[0])
	}

	// JSON object text is passed through structurally; scalar JSON stays a string.
	got = run(`{"type":"tool_result","tool_use_id":"t1","content":"{\"n\":1}"}`, "user", ids, tn)
	if gjson.Get(got[0], "functionResponse.response.result.n").Int() != 1 {
		t.Fatalf("object result lost structure: %s", got[0])
	}
	got = run(`{"type":"tool_result","tool_use_id":"t1","content":"123"}`, "user", ids, tn)
	if gjson.Get(got[0], "functionResponse.response.result").Type != gjson.String {
		t.Fatalf("scalar must stay string: %s", got[0])
	}

	// Images in tool_result content are returned as sibling parts.
	img := `{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"see"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + tinyPNGB64 + `"}}]}`
	got = run(img, "user", ids, tn)
	if len(got) < 2 || !gjson.Get(got[1], "inlineData").Exists() {
		t.Fatalf("image sibling part missing: %v", got)
	}
}

func TestCovContextWindowCeilingAndOutputCeiling(t *testing.T) {
	if got := ContextWindowCeiling("totally-unknown-model-xyz"); got != 1048576 {
		t.Fatalf("unknown model ceiling = %d", got)
	}
	if got := resolveContextWindowCeiling("gemini-4-argon"); got != 2097152 {
		t.Fatalf("fallback ceiling = %d", got)
	}
	if got := outputCeiling(accounts.AvailableModelInfo{MaxOutput: 12345}, true); got != 12345 {
		t.Fatalf("got %d", got)
	}
	if outputCeiling(accounts.AvailableModelInfo{MaxOutput: 12345}, false) != 64000 ||
		outputCeiling(accounts.AvailableModelInfo{}, true) != 64000 {
		t.Fatal("default output ceiling is 64000")
	}
}

func TestCovTieringInputMap(t *testing.T) {
	req := claude.Request{
		Model:        "claude-opus-4-5",
		OutputConfig: json.RawMessage(`{"effort":"high"}`),
		Thinking:     json.RawMessage(`{"type":"enabled","budget_tokens":2000}`),
		Messages: []claude.Message{
			{Role: claude.RoleUser, OutputConfig: json.RawMessage(`{"effort":"low"}`)},
			{Role: claude.RoleAssistant, OutputConfig: json.RawMessage(`not json`)},
		},
	}
	m := tieringInputMap(req)
	if m["model"] != "claude-opus-4-5" {
		t.Fatalf("model: %v", m["model"])
	}
	if oc, _ := m["output_config"].(map[string]interface{}); oc["effort"] != "high" {
		t.Fatalf("output_config: %v", m["output_config"])
	}
	if th, _ := m["thinking"].(map[string]interface{}); th["type"] != "enabled" {
		t.Fatalf("thinking: %v", m["thinking"])
	}
	msgs, _ := m["messages"].([]interface{})
	if len(msgs) != 2 {
		t.Fatalf("messages: %v", m["messages"])
	}
	m0 := msgs[0].(map[string]interface{})
	if m0["role"] != "user" || m0["output_config"].(map[string]interface{})["effort"] != "low" {
		t.Fatalf("msg0: %v", m0)
	}
	if _, has := msgs[1].(map[string]interface{})["output_config"]; has {
		t.Fatal("invalid per-message output_config must be dropped")
	}

	bad := tieringInputMap(claude.Request{Model: "m", OutputConfig: json.RawMessage(`[1]`), Thinking: json.RawMessage(`"x"`)})
	if _, ok := bad["output_config"]; ok {
		t.Fatal("non-object output_config must be dropped")
	}
	if _, ok := bad["thinking"]; ok {
		t.Fatal("non-object thinking must be dropped")
	}
	if _, ok := bad["messages"]; ok {
		t.Fatal("no messages -> no key")
	}
}

func TestCovRecordLoopNudgeKeyChoice(t *testing.T) {
	recordLoopNudge("cov-sess-a", "cov-loop-a", "n1")
	if got := loopguard.TakePendingNudges("cov-loop-a"); len(got) != 1 || got[0] != "n1" {
		t.Fatalf("loop key bucket: %v", got)
	}
	if got := loopguard.TakePendingNudges("cov-sess-a"); len(got) != 0 {
		t.Fatalf("session bucket must stay empty when loopKey is set: %v", got)
	}
	recordLoopNudge("cov-sess-b", "", "n2")
	if got := loopguard.TakePendingNudges("cov-sess-b"); len(got) != 1 || got[0] != "n2" {
		t.Fatalf("session fallback: %v", got)
	}
	recordLoopNudge("", "", "n3") // nowhere to store: must not panic or leak
	if got := loopguard.TakePendingNudges(""); got != nil {
		t.Fatalf("got %v", got)
	}
}
