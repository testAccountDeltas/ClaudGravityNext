package orchestrate

import (
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

// Non-streaming: a functionCall next to text is dropped for a request that
// forbids tool calls, and the turn ends as an ordinary text answer.
func TestNonStreaming_ForbiddenToolCallIsDropped(t *testing.T) {
	claude := `{"model":"claude-sonnet-4-5","max_tokens":1024,"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}],"messages":[{"role":"user","content":"go on"}]}`
	cand := tfWrap(`{"content":{"role":"model","parts":[{"text":"Reading sealing.go"},{"functionCall":{"name":"Read","args":{"file_path":"/a.go"}}}]},"finishReason":"STOP"}`)
	run := func(suppress bool) string {
		r := runExec(t, execOpts{nAcc: 1, claude: claude, suppressTools: suppress,
			up: func(int, *http.Request, string) (*http.Response, error) { return respBody(200, cand), nil }})
		if !r.ok {
			t.Fatalf("status %d err %v", r.status, r.err)
		}
		return r.body
	}
	body := run(true)
	if gjson.Get(body, `content.#(type=="tool_use")`).Exists() || gjson.Get(body, "stop_reason").String() != "end_turn" {
		t.Fatalf("the call must be dropped and the turn end as text: %s", body)
	}
	if gjson.Get(body, `content.#(type=="text").text`).String() != "Reading sealing.go" {
		t.Fatalf("the text must survive: %s", body)
	}
	body = run(false)
	if !gjson.Get(body, `content.#(type=="tool_use")`).Exists() {
		t.Fatalf("an ordinary request keeps its call: %s", body)
	}
}
