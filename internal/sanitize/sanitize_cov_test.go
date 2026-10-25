package sanitize

import (
	"encoding/json"
	"strings"
	"testing"

	"claudgravity/internal/editrepair"
	"claudgravity/internal/protocol/claude"
)

func covMsgs(t *testing.T, raw string) []claude.Message {
	t.Helper()
	req := mustReq(t, `{"model":"m","max_tokens":1,"messages":`+raw+`}`)
	return req.Messages
}

func resultText(t *testing.T, b claude.Block) string {
	t.Helper()
	tr, ok := b.AsToolResult()
	if !ok {
		t.Fatalf("not a tool_result: %+v", b)
	}
	return toolResultPlainText(tr.Content)
}

func TestDelayedToolResultText(t *testing.T) {
	if got := delayedToolResultText("t1", []byte(`"out"`)); got != "[Delayed tool output for t1]: out" {
		t.Fatalf("got %q", got)
	}
	if got := delayedToolResultText("t1", nil); got != "[Delayed tool output for t1]" {
		t.Fatalf("got %q", got)
	}
}

func TestOrphanedToolResultText(t *testing.T) {
	if got := orphanedToolResultText("t2", []byte(`[{"type":"text","text":"a"},"b"]`)); got != "[Tool output t2]: ab" {
		t.Fatalf("got %q", got)
	}
	if got := orphanedToolResultText("t2", []byte(`[]`)); got != "[Tool output t2]" {
		t.Fatalf("got %q", got)
	}
}

func TestSynthesize_StrayResultDegradedToDelayedText(t *testing.T) {
	msgs := covMsgs(t, `[
		{"role":"assistant","content":[{"type":"tool_use","id":"A","name":"Bash","input":{}}]},
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"OLD","content":"late data"},
			{"type":"tool_result","tool_use_id":"A","content":"ok"}]}
	]`)
	out, modified := synthesizeAbandonedToolResults(msgs)
	if !modified || len(out) != 2 {
		t.Fatalf("modified=%v len=%d", modified, len(out))
	}
	blocks := out[1].Content
	if len(blocks) != 2 {
		t.Fatalf("blocks=%d", len(blocks))
	}
	if resultText(t, blocks[0]) != "ok" {
		t.Fatalf("matched result should come first: %+v", blocks[0])
	}
	if got := textOf(t, blocks[1]); got != "[Delayed tool output for OLD]: late data" {
		t.Fatalf("got %q", got)
	}
}

func TestStripSyntheticXMLTags(t *testing.T) {
	cases := map[string]string{
		"plain": "plain",
		"a<task-notification>b</task-notification>c": "abc",
		"<TASK_RESULT>x</Task_Result>":               "x",
		"mentions task- but no tag":                  "mentions task- but no tag",
	}
	for in, want := range cases {
		if got := StripSyntheticXMLTags(in); got != want {
			t.Errorf("StripSyntheticXMLTags(%q)=%q want %q", in, got, want)
		}
	}
}

func TestPartialSyntheticTagSuffix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"no angle", ""},
		{"hello <task-", "<task-"},
		{"x </task-notif", "</task-notif"},
		{"y <TASK_res", "<TASK_res"},
		{"z <task-result>", ""},
		{"z <div", ""},
		{"<", "<"},
	}
	for _, c := range cases {
		if got := PartialSyntheticTagSuffix(c.in); got != c.want {
			t.Errorf("PartialSyntheticTagSuffix(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestExtractPlainText(t *testing.T) {
	msgs := covMsgs(t, `[{"role":"user","content":[
		{"type":"text","text":"one"},
		{"type":"tool_use","id":"x","name":"n","input":{}},
		{"type":"text","text":"two\n"},
		{"type":"text","text":"three"}]}]`)
	if got := extractPlainText(msgs[0].Content); got != "one\ntwo\nthree" {
		t.Fatalf("got %q", got)
	}
	if got := extractPlainText(nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestFirstNonEmptyString(t *testing.T) {
	m := map[string]interface{}{"a": "", "b": 5, "c": "val"}
	if got := firstNonEmptyString(m, "a", "b", "c"); got != "val" {
		t.Fatalf("got %q", got)
	}
	if got := firstNonEmptyString(m, "a", "zz"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func readPair(id, input, result string) string {
	return `{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"Read","input":` + input + `}]},
	{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":` + result + `}]}`
}

func TestRecordReadToolUse_Keys(t *testing.T) {
	mk := func(raw string) claude.Block {
		var b claude.Block
		if err := json.Unmarshal([]byte(raw), &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	ids := map[string]string{}
	recordReadToolUse(mk(`{"type":"tool_use","id":"1","name":"read","input":{"path":"/a/b.go","offset":3,"limit":10,"pages":"1-2"}}`), ids, "")
	if got := ids["1"]; !strings.HasSuffix(got, "b.go:off=3:lim=10:pages=1-2") {
		t.Fatalf("key=%q", got)
	}
	recordReadToolUse(mk(`{"type":"tool_use","id":"2","name":"Read","input":{"file":"rel.go"}}`), ids, `C:\proj`)
	if got := ids["2"]; !strings.Contains(got, "rel.go") || !strings.Contains(strings.ToLower(got), "proj") {
		t.Fatalf("key=%q", got)
	}
	// Ignored: non-Read tool, empty id, no path, null input, bad input.
	recordReadToolUse(mk(`{"type":"tool_use","id":"3","name":"Write","input":{"file_path":"/x"}}`), ids, "")
	recordReadToolUse(mk(`{"type":"tool_use","id":"","name":"Read","input":{"file_path":"/x"}}`), ids, "")
	recordReadToolUse(mk(`{"type":"tool_use","id":"5","name":"Read","input":{}}`), ids, "")
	recordReadToolUse(mk(`{"type":"tool_use","id":"6","name":"Read","input":null}`), ids, "")
	recordReadToolUse(mk(`{"type":"tool_use","id":"7","name":"Read","input":[1]}`), ids, "")
	recordReadToolUse(mk(`{"type":"text","text":"hi"}`), ids, "")
	if len(ids) != 2 {
		t.Fatalf("unexpected keys: %v", ids)
	}
}

func TestDedup_RelativeAndAbsoluteSameFile(t *testing.T) {
	const sid = "cov-dedup-cwd"
	cwd := t.TempDir()
	editrepair.RecordSessionCWD(sid, cwd)
	abs := editrepair.JoinCWD(cwd, "main.go")
	input1 := `{"file_path":` + mustJSON(abs) + `}`
	msgs := covMsgs(t, `[`+
		readPair("r1", input1, `"package main"`)+`,`+
		readPair("r2", `{"file_path":"main.go"}`, `"package main"`)+`]`)
	out, changed := dedupeRepeatedReads(msgs, sid)
	if !changed {
		t.Fatal("expected dedup")
	}
	if got := resultText(t, out[3].Content[0]); !strings.HasPrefix(got, "[Identical to the Read result") {
		t.Fatalf("got %q", got)
	}
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestDedup_SkipsErrorsMarkersAndNotices(t *testing.T) {
	// Nudge suffix (with CRLF before it) is stripped before comparing.
	msgs := covMsgs(t, `[`+
		readPair("a", `{"file_path":"/f"}`, `"x\r\ny\r\n"`)+`,`+
		readPair("b", `{"file_path":"/f"}`, `"x\r\ny\r\n\n\n[Notice from the gateway: hi"`)+`]`)
	out, changed := dedupeRepeatedReads(msgs, "cov-none")
	if !changed {
		t.Fatal("nudge-suffixed re-read should dedup")
	}
	if !strings.HasPrefix(resultText(t, out[3].Content[0]), "[Identical") {
		t.Fatal("not collapsed")
	}

	// Only a nudge: empty after stripping, left alone.
	msgs = covMsgs(t, `[`+readPair("a", `{"file_path":"/g"}`, `"\n\n[Notice from the gateway: only"`)+`]`)
	if _, changed = dedupeRepeatedReads(msgs, ""); changed {
		t.Fatal("empty-after-strip must not change")
	}

	// Already-marked result is never cached; unknown tool id and error ignored.
	msgs = covMsgs(t, `[`+
		readPair("a", `{"file_path":"/h"}`, `"[Identical to the Read result already shown earlier x"`)+`,`+
		readPair("b", `{"file_path":"/h"}`, `"[Identical to the Read result already shown earlier x"`)+`,
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"zzz","content":"q"},
		{"type":"tool_result","tool_use_id":"b","is_error":true,"content":"q"},
		{"type":"tool_result","tool_use_id":"b","content":""}]}]`)
	if _, changed = dedupeRepeatedReads(msgs, ""); changed {
		t.Fatal("nothing should change")
	}
}

func TestDedup_StaleReadNotCollapsed(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`[` + readPair("a", `{"file_path":"/s"}`, `"same"`))
	for i := 0; i < readDecayTurns+2; i++ {
		sb.WriteString(`,{"role":"assistant","content":"filler"},{"role":"user","content":"more"}`)
	}
	sb.WriteString(`,` + readPair("b", `{"file_path":"/s"}`, `"same"`) + `,` + readPair("c", `{"file_path":"/s"}`, `"same"`) + `]`)
	msgs := covMsgs(t, sb.String())
	out, changed := dedupeRepeatedReads(msgs, "")
	if !changed {
		t.Fatal("third read (within the refreshed window) should collapse")
	}
	n := len(out)
	if strings.HasPrefix(resultText(t, out[n-3].Content[0]), "[Identical") {
		t.Fatal("stale read must be returned in full")
	}
	if !strings.HasPrefix(resultText(t, out[n-1].Content[0]), "[Identical") {
		t.Fatal("follow-up read should collapse")
	}
}
