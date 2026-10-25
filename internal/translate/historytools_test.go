package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
)

const taskHistory = `"tools":[{"name":"Read","description":"r","input_schema":{"type":"object","properties":{}}}],` +
	`"messages":[{"role":"user","content":"go"},` +
	`{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"TaskCreate","input":{"subject":"Domain 1","count":2}}]},` +
	`{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"Task #1 created"}]},` +
	`{"role":"user","content":"now mark it done"}]`

func TestDeclareToolsUsedInHistory_ReconstructsASchemaFromTheCalls(t *testing.T) {
	out := declareToolsUsedInHistory([]byte(`{"model":"m",` + taskHistory + `}`))
	tools := gjson.GetBytes(out, "tools").Array()
	// Read + TaskCreate + the task siblings TaskGet, TaskList, TaskUpdate.
	if len(tools) != 5 || tools[1].Get("name").String() != "TaskCreate" {
		t.Fatalf("tools = %s", gjson.GetBytes(out, "tools").Raw)
	}
	for i, want := range []string{"TaskGet", "TaskList", "TaskUpdate"} {
		if tools[2+i].Get("name").String() != want {
			t.Fatalf("sibling %d = %s, want %s", i, tools[2+i].Get("name").String(), want)
		}
	}
	sch := tools[1].Get("input_schema")
	if sch.Get("properties.subject.type").String() != "string" || sch.Get("properties.count.type").String() != "integer" {
		t.Fatalf("schema = %s", sch.Raw)
	}
	if sch.Get("required").Exists() {
		t.Fatalf("nothing may be required: %s", sch.Raw)
	}
}

func TestDeclareToolsUsedInHistory_LeavesOtherCasesAlone(t *testing.T) {
	same := func(label, js string) {
		t.Helper()
		if got := declareToolsUsedInHistory([]byte(js)); string(got) != js {
			t.Errorf("%s: changed to %s", label, got)
		}
	}
	// A failed call is not evidence that the client runs the tool.
	same("errored", `{"model":"m",`+strings.Replace(taskHistory, `"content":"Task #1 created"`, `"is_error":true,"content":"No such tool available"`, 1)+`}`)
	// Already declared.
	same("declared", `{"model":"m",`+strings.Replace(taskHistory, `"name":"Read"`, `"name":"TaskCreate"`, 1)+`}`)
	// Search tools the gateway removes on purpose stay removed.
	same("search tool", `{"model":"m",`+strings.Replace(taskHistory, `"name":"TaskCreate"`, `"name":"Grep"`, 1)+`}`)
	// No result at all: nothing proves it ran.
	same("no result", `{"model":"m","tools":[],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"TaskCreate","input":{}}]}]}`)
	same("no messages", `{"model":"m"}`)
}

// The end-to-end effect: the earlier call stays a real functionCall and the
// tool is declared to Gemini, so the model has a call pattern to continue
// rather than a text note to imitate.
func TestConvert_ToolUsedEarlierButMissingNowStaysCallable(t *testing.T) {
	req := `{"model":"claude-opus-4-5","max_tokens":64,` + taskHistory + `}`
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{}, "main", "main", "s", "i", true)
	if err != nil {
		t.Fatal(err)
	}
	body := string(res.GeminiBody)
	if strings.Contains(body, "omitted here") {
		t.Fatalf("no text note expected: %s", body)
	}
	var call, decl bool
	for _, c := range gjson.Get(body, "contents").Array() {
		if c.Get("parts.0.functionCall.name").String() == "TaskCreate" {
			call = true
		}
	}
	for _, d := range gjson.Get(body, "tools.0.functionDeclarations").Array() {
		if d.Get("name").String() == "TaskCreate" {
			decl = true
		}
	}
	if !call || !decl {
		t.Fatalf("functionCall kept=%v declared=%v in %s", call, decl, body)
	}
}
