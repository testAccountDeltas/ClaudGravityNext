package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
)

// toolResultTurn converts a conversation whose tool_result carries items and
// returns the final user turn: the functionResponse text and any inline parts.
func toolResultTurn(t *testing.T, items string) (response string, inline []gjson.Result) {
	t.Helper()
	SetAllowHistoryTruncation(false)
	body := `{"model":"claude-sonnet-4-5","max_tokens":1024,"tools":[{"name":"Open","input_schema":{"type":"object","properties":{}}}],"messages":[` +
		`{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Open","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[` + items + `]}]}]}`
	res, err := ConvertClaudeRequestToGemini([]byte(body), config.TenantConfig{DefaultModel: "gemini-3.8-flash"}, "", "main", "s-att", "", true)
	if err != nil {
		t.Fatal(err)
	}
	contents := gjson.GetBytes(res.GeminiBody, "contents").Array()
	for _, p := range contents[len(contents)-1].Get("parts").Array() {
		if fr := p.Get("functionResponse"); fr.Exists() {
			response += fr.Get("response").Raw
		}
		if p.Get("inlineData").Exists() {
			inline = append(inline, p)
		}
	}
	return response, inline
}

func TestToolResultAttachments_TextualSourcesEndUpInTheFunctionResponse(t *testing.T) {
	for _, c := range []struct {
		name, item string
		want       []string
	}{
		{"title and context", `{"type":"document","title":"Spec","context":"read me first","source":{"type":"text","media_type":"text/plain","data":"plain body"}}`,
			[]string{`Attached Document: \"Spec\"`, "Context: read me first", "plain body"}},
		{"base64 text document", `{"type":"document","source":{"type":"base64","media_type":"text/csv","data":"` + b64("a,b\n1,2") + `"}}`,
			[]string{"a,b"}},
		{"content source with typed and bare items", `{"type":"document","source":{"type":"content","content":[{"type":"text","text":"typed line "},"bare line"]}}`,
			[]string{"typed line", "bare line"}},
		{"content source as one string", `{"type":"document","source":{"type":"content","content":"single string"}}`,
			[]string{"single string"}},
		{"files api reference", `{"type":"document","source":{"type":"file","file_id":"file_123"}}`,
			[]string{"file_123", "not retrievable"}},
		{"unfetchable url", `{"type":"image","source":{"type":"url","url":"http://127.0.0.1:1/x.png"}}`,
			[]string{"could not fetch", "http://127.0.0.1:1/x.png"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp, inline := toolResultTurn(t, c.item)
			if len(inline) != 0 {
				t.Fatalf("textual attachment must not become inlineData: %v", inline)
			}
			for _, w := range c.want {
				if !strings.Contains(resp, w) {
					t.Errorf("function response lacks %q:\n%s", w, resp)
				}
			}
		})
	}
}

func TestToolResultAttachments_BinaryBecomesInlineDataAfterTheResponse(t *testing.T) {
	resp, inline := toolResultTurn(t, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"`+tinyPNGB64+`"}}`)
	if len(inline) != 1 || inline[0].Get("inlineData.mimeType").String() != "image/png" {
		t.Fatalf("want one image/png inline part, got %v (response %s)", inline, resp)
	}
}

func TestToolResultAttachments_EmptyOrUnknownSourcesAddNothing(t *testing.T) {
	resp, inline := toolResultTurn(t, `{"type":"document","source":{"type":"text","data":""}},`+
		`{"type":"document","source":{"type":"file"}},`+
		`{"type":"document","source":{"type":"url","url":""}},`+
		`{"type":"document","source":{"type":"mystery"}},`+
		`{"type":"text","text":"survivor"}`)
	if len(inline) != 0 || !strings.Contains(resp, "survivor") {
		t.Fatalf("empty/unknown sources must be ignored without losing neighbours: inline=%v resp=%s", inline, resp)
	}
	if strings.Contains(resp, "Attached") {
		t.Fatalf("nothing should have been announced: %s", resp)
	}
}
