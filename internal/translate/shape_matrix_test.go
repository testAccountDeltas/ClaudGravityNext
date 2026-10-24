package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/loopguard"
)

const toolTurn = `{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"Read","input":{"p":"x"}}]}`

func TestMessageShapeMatrix(t *testing.T) {
	img := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + tinyPNGB64 + `"}}`
	cases := []struct{ name, body string }{
		{"system array with cache_control", `{"model":"m","system":[{"type":"text","text":"sys1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"sys2"}],"messages":[{"role":"user","content":"hi"}]}`},
		{"consecutive user messages", `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"user","content":"b"},{"role":"user","content":[{"type":"text","text":"c"}]}]}`},
		{"consecutive assistant messages", `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"assistant","content":"c"},{"role":"user","content":"d"}]}`},
		{"assistant thinking only", `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"s"}]},{"role":"user","content":"b"}]}`},
		{"assistant empty text block", `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"text","text":""}]},{"role":"user","content":"b"}]}`},
		{"tool_result mixed with text", `{"model":"m",` + readTool + `,"messages":[{"role":"user","content":"a"},` + toolTurn + `,{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok"},{"type":"text","text":"and also this"}]}]}`},
		{"tool_result not first in user turn", `{"model":"m",` + readTool + `,"messages":[{"role":"user","content":"a"},` + toolTurn + `,{"role":"user","content":[{"type":"text","text":"before"},{"type":"tool_result","tool_use_id":"c1","content":"ok"}]}]}`},
		{"image in tool_result is_error", `{"model":"m",` + readTool + `,"messages":[{"role":"user","content":"a"},` + toolTurn + `,{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","is_error":true,"content":[{"type":"text","text":"boom"},` + img + `]}]}]}`},
		{"image-only tool_result", `{"model":"m",` + readTool + `,"messages":[{"role":"user","content":"a"},` + toolTurn + `,{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":[` + img + `]}]}]}`},
		{"trailing assistant prefill", `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"{\"k\":"}]}`},
		{"parallel calls one result missing", `{"model":"m",` + readTool + `,"messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"Read","input":{}},{"type":"tool_use","id":"c2","name":"Read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok"}]}]}`},
		{"empty user content", `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":[]}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := convertForTest(t, tc.body)
			if err != nil {
				t.Fatalf("convert: %v", err)
			}
			assertGeminiShape(t, res.GeminiBody)
		})
	}
}

func TestSystemArrayJoinsAllBlocks(t *testing.T) {
	res, err := convertForTest(t, `{"model":"m","system":[{"type":"text","text":"sys1","cache_control":{"type":"ephemeral"}},{"type":"text","text":"sys2"}],"messages":[{"role":"user","content":"hi"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	sys := gjson.GetBytes(res.GeminiBody, "system_instruction").Raw
	if !strings.Contains(sys, "sys1") || !strings.Contains(sys, "sys2") {
		t.Fatalf("system blocks lost: %s", sys)
	}
}

func TestPrefillTextRecordedAndRolesAlternate(t *testing.T) {
	res, err := convertForTest(t, `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"{\"k\":"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.PrefillText != `{"k":` {
		t.Fatalf("prefill not captured: %q", res.PrefillText)
	}
	cs := assertGeminiShape(t, res.GeminiBody)
	if cs[len(cs)-1].Get("role").String() != "user" {
		t.Fatalf("request must end on a user turn: %s", res.GeminiBody)
	}
}

func TestLoopNudgeWithTrailingPrefillKeepsRoles(t *testing.T) {
	sid := "sess-" + t.Name()
	key := "main|" + sid + "|"
	loopguard.RecordPendingNudge(key, loopguard.NudgeReadLoop)
	t.Cleanup(func() { loopguard.TakePendingNudges(key); loopguard.TakePendingNudges(sid) })
	res, err := convertForTest(t, `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"prefill:"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	assertGeminiShape(t, res.GeminiBody)
	// with a plain user tail the nudge text is injected and roles alternate too
	loopguard.RecordPendingNudge(key, loopguard.NudgeReadLoop)
	res, err = convertForTest(t, `{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":"c"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	assertGeminiShape(t, res.GeminiBody)
}
