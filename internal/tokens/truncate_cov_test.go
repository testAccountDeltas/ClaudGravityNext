package tokens

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"claudgravity/internal/protocol/claude"
)

func msgs(t *testing.T, raw ...string) []claude.Message {
	t.Helper()
	req := mustRequest(t, `{"model":"m","max_tokens":10,"messages":[`+strings.Join(raw, ",")+`]}`)
	return req.Messages
}

func textMsg(role string, n int) string {
	return fmt.Sprintf(`{"role":%q,"content":[{"type":"text","text":%q}]}`, role, strings.Repeat("x", n))
}

const imgBlock = `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}`

func TestToolUseIDsAndMessageHasAnyToolResult(t *testing.T) {
	ms := msgs(t,
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"n","input":{}},{"type":"tool_use","id":"","name":"n","input":{}},{"type":"server_tool_use","id":"s1","name":"web_search","input":{}},{"type":"text","text":"hi"}]}`,
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}`,
		`{"role":"user","content":[{"type":"web_search_tool_result","tool_use_id":"s1","content":[]}]}`,
		`{"role":"user","content":[{"type":"tool_use","id":"u","name":"n","input":{}}]}`,
		`{"role":"user","content":[{"type":"text","text":"plain"}]}`)
	if got := toolUseIDs(ms[0]); strings.Join(got, ",") != "t1,s1" {
		t.Errorf("toolUseIDs = %v", got)
	}
	if got := toolUseIDs(ms[3]); got != nil {
		t.Errorf("user-role tool_use must be ignored: %v", got)
	}
	cases := []struct {
		msg  int
		ids  []string
		want bool
	}{
		{1, []string{"t1"}, true},
		{1, []string{"zzz", "t1"}, true},
		{1, []string{"s1"}, false},
		{2, []string{"s1"}, true},
		{2, []string{"t1"}, false},
		{4, []string{"t1", "s1"}, false},
		{1, nil, false},
	}
	for _, c := range cases {
		if got := messageHasAnyToolResult(ms[c.msg], c.ids); got != c.want {
			t.Errorf("msg %d ids %v: got %v want %v", c.msg, c.ids, got, c.want)
		}
	}
}

func TestPruneOldAttachments(t *testing.T) {
	short := msgs(t, textMsg("user", 1), textMsg("assistant", 1), textMsg("user", 1))
	if out, changed := pruneOldAttachments(short); changed || len(out) != 3 {
		t.Error("fewer than four messages must be returned as is")
	}
	withImg := func(role string) string {
		return fmt.Sprintf(`{"role":%q,"content":[%s,{"type":"text","text":"keep"},{"type":"document","source":{"type":"url","url":"u"}}]}`, role, imgBlock)
	}
	ms := msgs(t, withImg("user"), withImg("user"), textMsg("assistant", 1), withImg("user"), withImg("user"), textMsg("assistant", 1))
	out, changed := pruneOldAttachments(ms)
	if !changed || len(out) != len(ms) {
		t.Fatalf("changed=%v len=%d", changed, len(out))
	}
	count := func(m claude.Message, typ string) int {
		n := 0
		for _, b := range m.Content {
			if b.Type == typ {
				n++
			}
		}
		return n
	}
	// index 0 and the last three messages (3,4,5) are protected; only 1 is shed.
	for i, wantAtt := range []int{1, 0, 0, 1, 1, 0} {
		if got := count(out[i], "image"); got != wantAtt {
			t.Errorf("msg %d images = %d want %d", i, got, wantAtt)
		}
	}
	if count(out[1], "document") != 0 || count(out[1], "text") != 3 {
		t.Errorf("msg 1 should have both attachments replaced by text placeholders: %+v", out[1].Content)
	}
	if !strings.Contains(string(out[1].Content[0].Raw), "Previous image/document attachment omitted") {
		t.Errorf("placeholder text: %s", out[1].Content[0].Raw)
	}
	if count(ms[1], "image") != 1 {
		t.Error("input messages must not be mutated")
	}
	_, changed = pruneOldAttachments(msgs(t, textMsg("user", 1), textMsg("assistant", 1), textMsg("user", 1), textMsg("assistant", 1), textMsg("user", 1)))
	if changed {
		t.Error("no attachments -> unchanged")
	}
}

func TestTruncateConversationToFit_SheddingAttachmentsIsEnough(t *testing.T) {
	req := claude.Request{Messages: msgs(t,
		textMsg("user", 20),
		fmt.Sprintf(`{"role":"user","content":[%s]}`, imgBlock),
		textMsg("assistant", 20), textMsg("user", 20), textMsg("assistant", 20), textMsg("user", 20))}
	// Budget 600: the image alone prices at ~1100 tokens, the rest is tiny.
	got, dropped, truncated := TruncateConversationToFit(req, 2600, 0)
	if !truncated || dropped != 0 || len(got.Messages) != 6 {
		t.Fatalf("truncated=%v dropped=%d len=%d", truncated, dropped, len(got.Messages))
	}
	if got.Messages[1].Content[0].Type != "text" {
		t.Errorf("image should be shed to a placeholder: %s", got.Messages[1].Content[0].Raw)
	}
}

func TestTruncateConversationToFit_DropsToolAndSearchPairsAtomically(t *testing.T) {
	big := strings.Repeat("x", 2500)
	req := claude.Request{Messages: msgs(t,
		textMsg("user", 10),
		`{"role":"assistant","content":[{"type":"server_tool_use","id":"s1","name":"web_search","input":{}}]}`,
		fmt.Sprintf(`{"role":"user","content":[{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","title":"t","url":"u","snippet":%q}]}]}`, big),
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]}`,
		fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":%q}]}`, big),
		textMsg("assistant", 10), textMsg("user", 10))}
	got, dropped, truncated := TruncateConversationToFit(req, 3000, 0)
	if !truncated || dropped != 4 {
		t.Fatalf("truncated=%v dropped=%d", truncated, dropped)
	}
	var roles []string
	for _, m := range got.Messages {
		roles = append(roles, string(m.Role))
	}
	if strings.Join(roles, ",") != "user,assistant,user" {
		t.Errorf("roles after drop = %v", roles)
	}
	raw, _ := json.Marshal(got.Messages[0])
	if !strings.Contains(string(raw), "Earlier intermediate messages were dropped") {
		t.Errorf("notice missing: %s", raw)
	}
	if len(req.Messages[0].Content) != 1 {
		t.Error("original request's first message must not be mutated")
	}
}

func TestTruncateConversationToFit_NothingDroppableAndTinyHistory(t *testing.T) {
	small := claude.Request{Messages: msgs(t, textMsg("user", 5000), textMsg("assistant", 5000), textMsg("user", 5000))}
	if got, d, tr := TruncateConversationToFit(small, 100, 0); tr || d != 0 || len(got.Messages) != 3 {
		t.Error("fewer than four messages must be left alone")
	}
	// A tool_use/tool_result unit that would reach into the protected tail
	// cannot be dropped, so nothing is.
	pair := claude.Request{Messages: msgs(t, textMsg("user", 5000),
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"n","input":{}}]}`,
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"`+strings.Repeat("y", 5000)+`"}]}`,
		textMsg("assistant", 5000))}
	if got, d, tr := TruncateConversationToFit(pair, 100, 0); tr || d != 0 || len(got.Messages) != 4 {
		t.Errorf("dropped=%d truncated=%v", d, tr)
	}
	// A plain four-message history under a degenerate budget (window minus
	// output is negative, so half the window is used) drops index 1 and
	// re-inserts a synthetic assistant turn to keep roles alternating.
	four := claude.Request{Messages: msgs(t, textMsg("user", 5000), textMsg("assistant", 5000), textMsg("user", 5000), textMsg("assistant", 5000))}
	got, d, tr := TruncateConversationToFit(four, 1000, 5000)
	if !tr || d != 1 || len(got.Messages) != 4 || got.Messages[1].Role != claude.RoleAssistant ||
		!strings.Contains(string(got.Messages[1].Content[0].Raw), "I understand. Continuing") {
		t.Errorf("dropped=%d truncated=%v msgs=%d", d, tr, len(got.Messages))
	}
}

func TestPruneToolResultItems_Guards(t *testing.T) {
	ph := claude.NewBlock(claude.TextBlock{Type: "text", Text: "gone"})
	drop := map[[3]int]bool{{0, 0, 1}: true}
	good := claude.Block{Type: "tool_result", Raw: json.RawMessage(`{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"a"},` + imgBlock + `]}`)}
	out := pruneToolResultItems(good, 0, 0, drop, ph)
	var obj struct {
		ToolUseID string `json:"tool_use_id"`
		Content   []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out.Raw, &obj); err != nil || obj.ToolUseID != "t" || obj.Content[1].Text != "gone" || obj.Content[0].Text != "a" {
		t.Errorf("pruned = %s (%v)", out.Raw, err)
	}
	for name, raw := range map[string]string{
		"invalid json":        `not json`,
		"string content":      `{"type":"tool_result","content":"text"}`,
		"missing content":     `{"type":"tool_result"}`,
		"item index mismatch": `{"type":"tool_result","content":[{"type":"text","text":"a"}]}`,
	} {
		in := claude.Block{Type: "tool_result", Raw: json.RawMessage(raw)}
		if got := pruneToolResultItems(in, 0, 0, drop, ph); string(got.Raw) != raw {
			t.Errorf("%s: block changed: %s", name, got.Raw)
		}
	}
}

func TestInlineSize_ViaInlineAttachmentBytes(t *testing.T) {
	data := strings.Repeat("A", 100)
	mk := func(typ, mt string) string {
		media := ""
		if mt != "" {
			media = fmt.Sprintf(`"media_type":%q,`, mt)
		}
		return fmt.Sprintf(`{"role":"user","content":[{"type":%q,"source":{"type":"base64",%s"data":%q}}]}`, typ, media, data)
	}
	cases := []struct {
		name, msg string
		want      int
	}{
		{"image with params and case", mk("image", "Image/JPEG; q=1"), 100},
		{"image default media type", mk("image", ""), 100},
		{"document default is pdf", mk("document", ""), 100},
		{"pdf document", mk("document", "application/pdf"), 100},
		{"svg is text", mk("image", "image/svg+xml"), 0},
		{"text document", mk("document", "text/plain"), 0},
		{"non-base64 source", `{"role":"user","content":[{"type":"image","source":{"type":"url","url":"u"}}]}`, 0},
		{"assistant attachments are ignored", strings.Replace(mk("image", "image/png"), `"user"`, `"assistant"`, 1), 0},
		{"nested in tool_result", fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}},{"type":"text","text":"x"}]}]}`, data), 100},
		{"tool_result with string content", `{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"s"}]}`, 0},
	}
	for _, c := range cases {
		req := claude.Request{Messages: msgs(t, c.msg)}
		if got := InlineAttachmentBytes(req); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestPruneAttachmentsByBytes_PrunesTopLevelOldestFirst(t *testing.T) {
	data := strings.Repeat("A", 1000)
	one := func() string {
		return fmt.Sprintf(`{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}]}`, data)
	}
	req := claude.Request{Messages: msgs(t, one(), textMsg("assistant", 1), one(), textMsg("assistant", 1), one())}
	out, n := PruneAttachmentsByBytes(req, 1500)
	if n != 2 {
		t.Fatalf("pruned %d", n)
	}
	if out.Messages[0].Content[0].Type != "text" || out.Messages[2].Content[0].Type != "text" || out.Messages[4].Content[0].Type != "image" {
		t.Error("oldest two attachments should be replaced, newest kept")
	}
	if req.Messages[0].Content[0].Type != "image" {
		t.Error("input must not be mutated")
	}
}
