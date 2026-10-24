package translate

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
)

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func convertForTest(t *testing.T, body string) (Result, error) {
	t.Helper()
	return ConvertClaudeRequestToGemini([]byte(body), config.TenantConfig{DefaultModel: "gemini-3.8-flash"}, "", "main", "sess-"+t.Name(), "", true)
}

// assertGeminiShape checks the structural invariants Gemini 400s on:
// non-empty contents starting with user and strictly alternating roles, no
// empty parts, and every functionResponse in the user turn directly after
// the model turn holding its functionCall (and vice versa).
func assertGeminiShape(t *testing.T, gemini []byte) []gjson.Result {
	t.Helper()
	if !gjson.ValidBytes(gemini) {
		t.Fatalf("invalid JSON out: %s", gemini)
	}
	cs := gjson.GetBytes(gemini, "contents").Array()
	if len(cs) == 0 {
		t.Fatalf("no contents: %s", gemini)
	}
	for i, c := range cs {
		want := "user"
		if i%2 == 1 {
			want = "model"
		}
		if c.Get("role").String() != want {
			t.Fatalf("contents[%d] role %q, want %q: %s", i, c.Get("role").String(), want, gemini)
		}
		parts := c.Get("parts").Array()
		if len(parts) == 0 {
			t.Fatalf("contents[%d] has no parts: %s", i, gemini)
		}
		calls, resps := 0, 0
		for j, p := range parts {
			if !p.IsObject() || len(p.Map()) == 0 {
				t.Fatalf("contents[%d].parts[%d] empty: %s", i, j, gemini)
			}
			if txt := p.Get("text"); txt.Exists() && txt.String() == "" && !p.Get("thought").Exists() && !p.Get("thoughtSignature").Exists() {
				t.Fatalf("contents[%d].parts[%d] empty text: %s", i, j, gemini)
			}
			if p.Get("functionCall").Exists() {
				calls++
			}
			if p.Get("functionResponse").Exists() {
				resps++
			}
		}
		if resps > 0 && (i == 0 || cs[i-1].Get("parts.#(functionCall)").Raw == "") {
			t.Fatalf("contents[%d] functionResponse without preceding functionCall turn: %s", i, gemini)
		}
		if calls > 0 {
			if i+1 >= len(cs) || cs[i+1].Get("parts.#(functionResponse)").Raw == "" {
				t.Fatalf("contents[%d] functionCall not answered by next turn: %s", i, gemini)
			}
			if n := len(cs[i+1].Get("parts.#(functionResponse)#").Array()); n != calls {
				t.Fatalf("contents[%d] has %d calls but next turn %d responses: %s", i, calls, n, gemini)
			}
		}
	}
	return cs
}

const readTool = `"tools":[{"name":"Read","description":"d","input_schema":{"type":"object","properties":{"p":{"type":"string"}}}}]`
