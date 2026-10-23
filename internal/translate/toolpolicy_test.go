package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
)

func convertTools(t *testing.T, body, model string, tenantMod func(*config.TenantConfig)) (Result, error) {
	t.Helper()
	tenant := config.TenantConfig{DefaultModel: model}
	if tenantMod != nil {
		tenantMod(&tenant)
	}
	return ConvertClaudeRequestToGemini([]byte(body), tenant, "", "main", "sess-"+t.Name(), "", true)
}

func declNames(res Result) []string {
	var out []string
	for _, d := range gjson.GetBytes(res.GeminiBody, "tools.0.functionDeclarations").Array() {
		out = append(out, d.Get("name").String())
	}
	return out
}

func hasName(names []string, n string) bool {
	for _, x := range names {
		if x == n {
			return true
		}
	}
	return false
}

const wsTool = `{"name":"WebSearch","description":"search","input_schema":{"type":"object","properties":{"query":{"type":"string"},"allowed_domains":{"type":"array","items":{"type":"string"}}},"required":["query"]}}`
const bashTool = `{"name":"Bash","description":"run","input_schema":{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}}`

func TestTools_ClientWebSearchDeclaredAsFunction_AndReplayedAsFunctionCall(t *testing.T) {
	body := `{"model":"claude-opus-4-5","tools":[` + bashTool + `,` + wsTool + `],"messages":[
		{"role":"user","content":"find"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"WebSearch","input":{"query":"go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"result"}]}]}`
	res, err := convertTools(t, body, "gemini-3.8-flash-tiered", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasName(declNames(res), "WebSearch") {
		t.Fatalf("client WebSearch must be a function declaration: %v", declNames(res))
	}
	if !strings.Contains(string(res.GeminiBody), `"functionCall":{"name":"WebSearch"`) &&
		!strings.Contains(string(res.GeminiBody), `"functionCall":{"args"`) {
		t.Fatalf("history WebSearch tool_use must replay as functionCall: %s", res.GeminiBody)
	}
	if strings.Contains(string(res.GeminiBody), "is omitted here") {
		t.Fatalf("WebSearch history replayed as prose: %s", res.GeminiBody)
	}
}

func TestTools_GrepGlobKeptOnGemini3_StrippedOnOlder_OptOutHonored(t *testing.T) {
	grep := `{"name":"Grep","description":"g","input_schema":{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"]}}`
	body := `{"model":"claude-opus-4-5","tools":[` + bashTool + `,` + grep + `],"messages":[{"role":"user","content":"x"}]}`
	on := func(c *config.TenantConfig) { c.StripSearchTools = true }
	res, err := convertTools(t, body, "gemini-3.8-flash-tiered", on)
	if err != nil || !hasName(declNames(res), "Grep") {
		t.Fatalf("Grep must be declared for gemini-3.x by default: %v %v", declNames(res), err)
	}
	res, err = convertTools(t, body, "gemini-3.8-flash-tiered", func(c *config.TenantConfig) {
		c.StripSearchTools, c.StripSearchToolsGemini3 = true, true
	})
	if err != nil || hasName(declNames(res), "Grep") {
		t.Fatalf("opt-out must strip Grep on gemini-3.x: %v %v", declNames(res), err)
	}
}

func TestTools_ToolChoiceValidation(t *testing.T) {
	mk := func(tc string) string {
		return `{"model":"claude-opus-4-5","tools":[` + bashTool + `],"tool_choice":` + tc + `,"messages":[{"role":"user","content":"x"}]}`
	}
	if _, err := convertTools(t, mk(`{"type":"tool","name":"Nope"}`), "gemini-3.8-flash-tiered", nil); err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Fatalf("undeclared forced tool must error, got %v", err)
	}
	if _, err := convertTools(t, mk(`"required"`), "gemini-3.8-flash-tiered", nil); err == nil {
		t.Fatal(`tool_choice "required" must error`)
	}
	if _, err := convertTools(t, mk(`{"type":"tool","name":"Bash"}`), "gemini-3.8-flash-tiered", nil); err != nil {
		t.Fatalf("declared forced tool must pass: %v", err)
	}
}

func TestTools_UnknownServerToolsDropped_BuiltinsKept(t *testing.T) {
	body := `{"model":"claude-opus-4-5","tools":[` + bashTool + `,{"type":"advisor_20260301","name":"advisor"},{"type":"text_editor_20250728","name":"str_replace_based_edit_tool"}],"messages":[{"role":"user","content":"x"}]}`
	res, err := convertTools(t, body, "gemini-3.8-flash-tiered", nil)
	if err != nil {
		t.Fatal(err)
	}
	names := declNames(res)
	if hasName(names, "advisor") || !hasName(names, "str_replace_based_edit_tool") || !hasName(names, "Bash") {
		t.Fatalf("declared = %v", names)
	}
	if res.ReverseToolNameMap["str_replace_editor"] != "str_replace_based_edit_tool" {
		t.Fatalf("editor alias missing: %v", res.ReverseToolNameMap)
	}
}

func TestTools_ReverseMapRepairsNameCase(t *testing.T) {
	body := `{"model":"claude-opus-4-5","tools":[` + bashTool + `,{"name":"Read","description":"r","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}],"messages":[{"role":"user","content":"x"}]}`
	res, err := convertTools(t, body, "gemini-3.8-flash-tiered", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.ReverseToolNameMap["read"] != "Read" || res.ReverseToolNameMap["bash"] != "Bash" {
		t.Fatalf("case repair map missing: %v", res.ReverseToolNameMap)
	}
}

func TestToolUse_ParallelCalls_SignatureOnlyOnFirst(t *testing.T) {
	body := `{"model":"claude-opus-4-5","tools":[` + bashTool + `],"messages":[
		{"role":"user","content":"x"},
		{"role":"assistant","content":[
			{"type":"tool_use","id":"toolu_a","name":"Bash","input":{"command":"ls"}},
			{"type":"tool_use","id":"toolu_b","name":"Bash","input":{"command":"pwd"}}]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"toolu_a","content":"a"},
			{"type":"tool_result","tool_use_id":"toolu_b","content":"b"}]}]}`
	res, err := convertTools(t, body, "gemini-3.8-flash-tiered", nil)
	if err != nil {
		t.Fatal(err)
	}
	var calls []gjson.Result
	for _, c := range gjson.GetBytes(res.GeminiBody, "contents").Array() {
		for _, p := range c.Get("parts").Array() {
			if p.Get("functionCall").Exists() {
				calls = append(calls, p)
			}
		}
	}
	if len(calls) != 2 {
		t.Fatalf("want 2 functionCalls, got %d in %s", len(calls), res.GeminiBody)
	}
	if !calls[0].Get("thoughtSignature").Exists() || calls[1].Get("thoughtSignature").Exists() {
		t.Fatalf("signature must be on the first call only: %s / %s", calls[0].Raw, calls[1].Raw)
	}
}
