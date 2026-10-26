package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
)

const summaryTools = `"tools":[{"name":"Read","description":"r","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}]`

func summaryReq(lastUser string) string {
	return `{"model":"claude-opus-4-5","max_tokens":64,` + summaryTools + `,"messages":[` +
		`{"role":"user","content":"investigate"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package a"},` + lastUser + `]}]}`
}

func TestAgentSummaryFork_ToolCallsAreForbiddenButStayDeclared(t *testing.T) {
	prompt := `{"type":"text","text":"Describe your most recent action in 3-5 words using present tense (-ing). Name the file or function, not the branch. Do not use tools."}`
	res, err := ConvertClaudeRequestToGemini([]byte(summaryReq(prompt)), config.TenantConfig{}, "auxiliary", "auxiliary", "s", "i", true)
	if err != nil {
		t.Fatal(err)
	}
	body := string(res.GeminiBody)
	if got := gjson.Get(body, "toolConfig.functionCallingConfig.mode").String(); got != "NONE" {
		t.Fatalf("mode = %q, want NONE", got)
	}
	if len(gjson.Get(body, "tools.0.functionDeclarations").Array()) == 0 {
		t.Fatalf("the declarations must stay (they keep the cached prefix valid): %s", body)
	}
}

func TestAgentSummaryFork_OutputIsBounded(t *testing.T) {
	prompt := `{"type":"text","text":"Describe your most recent action in 3-5 words using present tense (-ing). Do not use tools."}`
	req := summaryReq(prompt)
	req = strings.Replace(req, `"max_tokens":64`, `"max_tokens":128000`, 1)
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{}, "auxiliary", "auxiliary", "s", "i", true)
	if err != nil {
		t.Fatal(err)
	}
	got := gjson.GetBytes(res.GeminiBody, "generationConfig.maxOutputTokens").Int()
	if got <= 0 || got > agentSummaryAnswerRoom+int64(res.ThinkingBudget)+1 {
		t.Fatalf("maxOutputTokens = %d (thinking budget %d)", got, res.ThinkingBudget)
	}
	// An ordinary request keeps its own allowance.
	plain := strings.Replace(req, "Describe your most recent action in 3-5 words using present tense (-ing). Do not use tools.", "continue", 1)
	res, _ = ConvertClaudeRequestToGemini([]byte(plain), config.TenantConfig{}, "main", "main", "s", "i", true)
	if gjson.GetBytes(res.GeminiBody, "generationConfig.maxOutputTokens").Int() <= agentSummaryAnswerRoom+int64(res.ThinkingBudget) {
		t.Fatalf("a normal request must not be capped: %s", gjson.GetBytes(res.GeminiBody, "generationConfig").Raw)
	}
}

func TestAgentSummaryFork_OrdinaryRequestsKeepToolCalling(t *testing.T) {
	plain := `{"type":"text","text":"continue with the audit"}`
	res, err := ConvertClaudeRequestToGemini([]byte(summaryReq(plain)), config.TenantConfig{}, "main", "main", "s", "i", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(res.GeminiBody, "toolConfig.functionCallingConfig.mode").String(); got == "NONE" {
		t.Fatalf("a normal request must not lose tool calling: %s", res.GeminiBody)
	}
}

func TestIsAgentSummaryFork_Shapes(t *testing.T) {
	cases := map[string]bool{
		`{"messages":[{"role":"user","content":"Describe your most recent action in 3-5 words using present tense"}]}`:                                                             true,
		`{"messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"text","text":"Describe your most recent action in 3-5 words using present tense (-ing)."}]}]}`: true,
		`{"messages":[{"role":"assistant","content":"Describe your most recent action in 3-5 words using present tense"}]}`:                                                        false,
		`{"messages":[{"role":"user","content":"Describe your most recent action"}]}`:                                                                                              false,
		`{"messages":[]}`: false,
		`{}`:              false,
	}
	for js, want := range cases {
		if got := isAgentSummaryFork(gjson.Parse(js)); got != want {
			t.Errorf("%s: got %v, want %v", js, got, want)
		}
	}
}
