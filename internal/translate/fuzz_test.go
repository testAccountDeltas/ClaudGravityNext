package translate

import (
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
)

// FuzzConvertClaudeRequestToGemini asserts the converter never panics and
// that a successful conversion is valid JSON. Conversion errors (bad JSON,
// context overflow) are legitimate returns.
func FuzzConvertClaudeRequestToGemini(f *testing.F) {
	img := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + tinyPNGB64 + `"}}`
	seeds := []string{
		`{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"m","system":[{"type":"text","text":"s","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"a"},{"role":"assistant","content":"{"}]}`,
		`{"model":"m","messages":[{"role":"user","content":"a"},` + toolTurn + `,{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","is_error":true,"content":[{"type":"text","text":"x"},` + img + `]},{"type":"text","text":"t"}]}],` + readTool + `}`,
		`{"model":"m","thinking":{"type":"enabled","budget_tokens":2000},"messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"thinking","thinking":"h","signature":"s"}]},{"role":"user","content":"b"}]}`,
		`{"model":"m","tool_choice":{"type":"tool","name":"Read"},"messages":[{"role":"user","content":[{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"` + tinyPDFB64 + `"}}]}],` + readTool + `}`,
		`{"model":"m","messages":[{"role":"user","content":[{"type":"web_search_tool_result","tool_use_id":"w","content":[]},{"type":"unknown"}]}]}`,
		`{"messages":null}`, `{}`, `[]`, `{"messages":[{"role":"user"}]}`, `{"messages":[{"role":"system","content":5}]}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		res, err := ConvertClaudeRequestToGemini(body, config.TenantConfig{DefaultModel: "gemini-3.8-flash"}, "", "main", "sess-fuzz", "", true)
		if err != nil {
			return
		}
		if !gjson.ValidBytes(res.GeminiBody) {
			t.Fatalf("invalid Gemini JSON for %q: %s", body, res.GeminiBody)
		}
	})
}
