package sanitize

import (
	"encoding/json"
	"testing"

	"claudgravity/internal/protocol/claude"
)

func textMsg(role claude.Role, text, outputConfig string) claude.Message {
	m := claude.Message{Role: role, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: text})}}
	if outputConfig != "" {
		m.OutputConfig = json.RawMessage(outputConfig)
	}
	return m
}

func msgText(t *testing.T, m claude.Message) string {
	t.Helper()
	if len(m.Content) == 0 {
		return ""
	}
	tb, ok := m.Content[0].AsText()
	if !ok {
		t.Fatalf("first block is not text: %s", m.Content[0].Raw)
	}
	return tb.Text
}

func TestNormalizeMidConversationSystemMessages(t *testing.T) {
	sys := claude.Role("system")

	out, mod := normalizeMidConversationSystemMessages([]claude.Message{textMsg(claude.RoleUser, "hi", ""), textMsg(claude.RoleAssistant, "yo", "")})
	if mod || len(out) != 2 {
		t.Fatalf("a conversation without system turns is unchanged: mod=%v len=%d", mod, len(out))
	}

	out, mod = normalizeMidConversationSystemMessages([]claude.Message{
		textMsg(claude.RoleUser, "hi", ""), textMsg(sys, "  remember X  ", `{"effort":"low"}`), textMsg(claude.RoleAssistant, "ok", "")})
	if !mod || len(out) != 3 || out[1].Role != claude.RoleUser {
		t.Fatalf("a mid-conversation system turn becomes a user turn: mod=%v out=%+v", mod, out)
	}
	if got := msgText(t, out[1]); got != "[System Note]: remember X" {
		t.Fatalf("text = %q", got)
	}
	if string(out[1].OutputConfig) != `{"effort":"low"}` {
		t.Fatalf("its own output_config travels with it, got %q", out[1].OutputConfig)
	}
}

func TestNormalizeMidConversationSystemMessages_EmptySystemTurnHandsOverItsOutputConfig(t *testing.T) {
	sys := claude.Role("system")
	cfg := `{"effort":"high"}`

	// Dropped, and the next turn has no config of its own: it inherits.
	out, mod := normalizeMidConversationSystemMessages([]claude.Message{
		textMsg(claude.RoleUser, "a", ""), textMsg(sys, "   ", cfg), textMsg(claude.RoleAssistant, "b", "")})
	if !mod || len(out) != 2 || string(out[1].OutputConfig) != cfg {
		t.Fatalf("empty system turn must vanish and pass its config on: mod=%v out=%+v", mod, out)
	}

	// The next turn's own config wins.
	out, _ = normalizeMidConversationSystemMessages([]claude.Message{
		textMsg(claude.RoleUser, "a", ""), textMsg(sys, "", cfg), textMsg(claude.RoleAssistant, "b", `{"effort":"low"}`)})
	if string(out[1].OutputConfig) != `{"effort":"low"}` {
		t.Fatalf("an explicit config is never overwritten, got %q", out[1].OutputConfig)
	}

	// A literal null counts as "no config" and may be replaced.
	out, _ = normalizeMidConversationSystemMessages([]claude.Message{
		textMsg(claude.RoleUser, "a", ""), textMsg(sys, "", cfg), textMsg(claude.RoleAssistant, "b", `null`)})
	if string(out[1].OutputConfig) != cfg {
		t.Fatalf("null is not a config, got %q", out[1].OutputConfig)
	}

	// Nothing follows: the preceding turn gets it.
	out, _ = normalizeMidConversationSystemMessages([]claude.Message{textMsg(claude.RoleUser, "a", ""), textMsg(sys, "", cfg)})
	if len(out) != 1 || string(out[0].OutputConfig) != cfg {
		t.Fatalf("a trailing empty system turn hands its config back: %+v", out)
	}

	// Without any config, nothing is invented, and a null stays as sent.
	out, _ = normalizeMidConversationSystemMessages([]claude.Message{textMsg(claude.RoleUser, "a", `null`), textMsg(sys, "", ""), textMsg(claude.RoleAssistant, "b", "")})
	if string(out[0].OutputConfig) != `null` || len(out[1].OutputConfig) != 0 {
		t.Fatalf("no config to pass on, nothing may change: %q %q", out[0].OutputConfig, out[1].OutputConfig)
	}
}

func TestExtractLeadingSystemMessages(t *testing.T) {
	sys := claude.Role("system")
	req := claude.Request{Messages: []claude.Message{
		textMsg(sys, "rule one", ""), textMsg(sys, "  ", ""), textMsg(sys, "rule two", ""),
		textMsg(claude.RoleUser, "hello", ""), textMsg(sys, "later", "")}}
	msgs, mod := extractLeadingSystemMessages(&req)
	if !mod || len(msgs) != 2 || msgs[0].Role != claude.RoleUser {
		t.Fatalf("all leading system turns go, the mid one stays for the other pass: mod=%v msgs=%+v", mod, msgs)
	}
	var s string
	if err := json.Unmarshal(req.System, &s); err != nil || s != "rule one\n\nrule two" {
		t.Fatalf("system = %q (%v), want the two rules joined", req.System, err)
	}
	if _, mod := extractLeadingSystemMessages(&claude.Request{Messages: []claude.Message{textMsg(claude.RoleUser, "x", "")}}); mod {
		t.Fatal("nothing to extract, nothing modified")
	}
}

func TestAppendSystemText_Shapes(t *testing.T) {
	if got := string(appendSystemText(nil, "A")); got != `"A"` {
		t.Errorf("fresh: %s", got)
	}
	if got := string(appendSystemText(claude.SystemPrompt(`"base"`), "A")); got != `"base\n\nA"` {
		t.Errorf("string system: %s", got)
	}
	got := string(appendSystemText(claude.SystemPrompt(`[{"type":"text","text":"base"}]`), "A"))
	if got != `[{"type":"text","text":"base"},{"text":"A","type":"text"}]` {
		t.Errorf("block-array system: %s", got)
	}
}

func TestToolResultPlainText(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"bare string", `"hello"`, "hello"},
		{"text items are concatenated with no separator", `[{"type":"text","text":"a"},"b",{"type":"text","text":"c"}]`, "abc"},
		{"images contribute nothing", `[{"type":"image","source":{}},{"type":"text","text":"x"}]`, "x"},
		{"empty", ``, ""},
		{"object content has no plain text", `{"k":"v"}`, ""},
		{"number", `42`, ""},
	} {
		if got := toolResultPlainText(json.RawMessage(c.in)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
