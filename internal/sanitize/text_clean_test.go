package sanitize

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"claudgravity/internal/protocol/claude"
)

func TestStripANSIEscapeCodes(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"SGR colour", "\x1b[31mred\x1b[0m", "red"},
		{"256-colour and bare reset", "\x1b[38;5;196mX\x1b[m", "X"},
		{"erase and cursor movement", "\x1b[2K\x1b[1Gline", "line"},
		{"private mode: hide/show cursor", "\x1b[?25lhide\x1b[?25h", "hide"},
		{"private mode: bracketed paste", "\x1b[?2004htext\x1b[?2004l", "text"},
		{"OSC title ended by BEL", "\x1b]0;window title\x07text", "text"},
		{"OSC hyperlink ended by ST", "\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\", "link"},
		{"charset selection", "a\x1b(Bb\x1b(0c", "abc"},
		{"keypad mode", "\x1b=x\x1b>", "x"},
		{"no escapes: untouched", "plain [31m text", "plain [31m text"},
		{"unicode is not an escape", "привет 😀 €", "привет 😀 €"},
		{"empty", "", ""},
	} {
		if got := StripANSIEscapeCodes(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCleanTerminalText(t *testing.T) {
	for _, c := range []struct {
		name, in, want string
		changed        bool
	}{
		{"clean text is reported unchanged", "just text", "just text", false},
		{"ANSI", "\x1b[1mbold\x1b[0m", "bold", true},
		{"NUL", "a\x00b\x00", "ab", true},
		{"escape split by a NUL is still removed", "\x1b\x00[31mred", "red", true},
		{"both", "\x1b[31ma\x00b", "ab", true},
	} {
		got, changed := cleanTerminalText(c.in)
		if got != c.want || changed != c.changed {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, got, changed, c.want, c.changed)
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestTruncateMiddle_Boundary(t *testing.T) {
	exact := strings.Repeat("a", ToolOutputCharLimit)
	if out, did := TruncateMiddle(exact); did || out != exact {
		t.Fatal("text of exactly the limit must pass untouched")
	}
	if _, did := TruncateMiddle(exact + "a"); !did {
		t.Fatal("one byte over the limit must be truncated")
	}
}

func TestTruncateMiddle_KeepsHeadAndTailAndFitsTheLimit(t *testing.T) {
	var sb strings.Builder
	for sb.Len() < 3*ToolOutputCharLimit {
		sb.WriteString("0123456789abcdef")
	}
	text := sb.String()
	out, did := TruncateMiddle(text)
	if !did || len(out) > ToolOutputCharLimit {
		t.Fatalf("did=%v len=%d, want truncated within %d", did, len(out), ToolOutputCharLimit)
	}
	half := (ToolOutputCharLimit - 128) / 2
	if !strings.HasPrefix(out, text[:half]) || !strings.HasSuffix(out, text[len(text)-half:]) {
		t.Fatal("head and tail must be kept verbatim")
	}
	wantMarker := "\n<truncated " + itoa(len(text)-2*half) + " of " + itoa(len(text)) + " chars from the middle of the output>\n"
	if marker := out[half : len(out)-half]; marker != wantMarker {
		t.Fatalf("marker = %q, want %q", marker, wantMarker)
	}
	if again, redo := TruncateMiddle(out); redo || again != out {
		t.Fatal("an already truncated result must not be truncated again")
	}
}

func TestTruncateMiddle_NeverSplitsARune(t *testing.T) {
	for _, unit := range []string{"€", "я", "😀", "aé€😀"} {
		for pad := 0; pad < 4; pad++ {
			text := strings.Repeat("a", pad) + strings.Repeat(unit, ToolOutputCharLimit/len(unit)*2)
			out, did := TruncateMiddle(text)
			if !did {
				t.Fatalf("unit %q pad %d: expected truncation", unit, pad)
			}
			if !utf8.ValidString(out) {
				t.Fatalf("unit %q pad %d: result is not valid UTF-8", unit, pad)
			}
			if len(out) > ToolOutputCharLimit {
				t.Fatalf("unit %q pad %d: %d bytes exceeds the limit", unit, pad, len(out))
			}
		}
	}
}

func TestTruncateMiddle_JSONArrays(t *testing.T) {
	elem := func(i int) string { return `{"n":` + itoa(i) + `,"pad":"` + strings.Repeat("x", 100) + `"}` }
	var parts []string
	for i := 0; i < 2000; i++ {
		parts = append(parts, elem(i))
	}
	out, did := TruncateMiddle("[" + strings.Join(parts, ",") + "]")
	var got []json.RawMessage
	if !did || json.Unmarshal([]byte(out), &got) != nil || len(out) > ToolOutputCharLimit {
		t.Fatalf("a big array must stay a valid array within the limit: did=%v len=%d", did, len(out))
	}
	if string(got[0]) != elem(0) || string(got[len(got)-1]) != elem(1999) {
		t.Fatal("first and last elements must survive")
	}
	found := false
	for _, e := range got {
		var marker string
		if json.Unmarshal(e, &marker) == nil && strings.Contains(marker, "array elements truncated") {
			found = true
		}
	}
	if !found {
		t.Fatal("a marker element must say what was dropped")
	}

	// Too few elements, or a few huge ones: element-wise trimming gains
	// nothing, so the byte-middle rule applies instead (still valid UTF-8).
	huge := strings.Repeat("y", 40000)
	for name, in := range map[string]string{
		"three huge elements": `["` + huge + `","` + huge + `","` + huge + `"]`,
		"two elements":        `["` + huge + huge + `","` + huge + `"]`,
		"JSON object":         `{"k":"` + strings.Repeat("z", 3*ToolOutputCharLimit) + `"}`,
	} {
		o, did := TruncateMiddle(in)
		if !did || len(o) > ToolOutputCharLimit || !utf8.ValidString(o) || !strings.Contains(o, "<truncated ") {
			t.Errorf("%s: did=%v len=%d", name, did, len(o))
		}
	}
}

func toolResult(content string) claude.Block {
	return claude.NewBlock(claude.ToolResultBlock{Type: "tool_result", ToolUseID: "toolu_1", IsError: true,
		Content: json.RawMessage(content), CacheControl: json.RawMessage(`{"type":"ephemeral"}`)})
}

func resultContent(t *testing.T, b claude.Block) string {
	t.Helper()
	tr, ok := b.AsToolResult()
	if !ok {
		t.Fatal("not a tool_result")
	}
	return string(tr.Content)
}

func TestTruncateToolResult_StringContent(t *testing.T) {
	b, truncated, modified := truncateToolResult(toolResult(`"\u001b[31mred\u001b[0m and \u0000nul"`))
	if !modified || truncated || resultContent(t, b) != `"red and nul"` {
		t.Fatalf("modified=%v truncated=%v content=%s", modified, truncated, resultContent(t, b))
	}
	tr, _ := b.AsToolResult()
	if tr.ToolUseID != "toolu_1" || !tr.IsError || string(tr.CacheControl) != `{"type":"ephemeral"}` {
		t.Fatalf("id, error flag and cache control must be preserved: %+v", tr)
	}
	// An escape hidden by a NUL inside it.
	b, _, modified = truncateToolResult(toolResult(`"\u001b\u0000[31mX"`))
	if !modified || resultContent(t, b) != `"X"` {
		t.Fatalf("NUL-split escape: %s", resultContent(t, b))
	}
	// Nothing to do: the very same block comes back, flagged unmodified.
	orig := toolResult(`"clean"`)
	if got, tr, mod := truncateToolResult(orig); mod || tr || string(got.Raw) != string(orig.Raw) {
		t.Fatalf("a clean result must be returned as is: modified=%v", mod)
	}
}

func TestTruncateToolResult_ArrayContent(t *testing.T) {
	img := `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}`
	in := `[{"type":"text","text":"\u001b[?25lhi\u0000"},"\u001b[1mplain\u001b[0m\u0000",` + img + `,{"type":"text","text":"\u001b\u0000[31mok"}]`
	b, truncated, modified := truncateToolResult(toolResult(in))
	if !modified || truncated {
		t.Fatalf("modified=%v truncated=%v", modified, truncated)
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(resultContent(t, b)), &items); err != nil || len(items) != 4 {
		t.Fatalf("shape must be kept: %v %s", err, resultContent(t, b))
	}
	var t0, t3 struct{ Text string }
	var s1 string
	_ = json.Unmarshal(items[0], &t0)
	_ = json.Unmarshal(items[1], &s1)
	_ = json.Unmarshal(items[3], &t3)
	if t0.Text != "hi" || s1 != "plain" || t3.Text != "ok" {
		t.Fatalf("cleaned texts = %q %q %q", t0.Text, s1, t3.Text)
	}
	if string(items[2]) != img {
		t.Fatalf("an image item must be left byte-for-byte alone, got %s", items[2])
	}
}

func TestTruncateToolResult_Oversized(t *testing.T) {
	big := strings.Repeat("line of output\n", 20000)
	bigJSON, _ := json.Marshal(big)
	for name, content := range map[string]string{
		"string":       string(bigJSON),
		"array text":   `[{"type":"text","text":` + string(bigJSON) + `}]`,
		"array string": `[` + string(bigJSON) + `]`,
		"object":       `{"result":` + string(bigJSON) + `}`,
	} {
		b, truncated, modified := truncateToolResult(toolResult(content))
		if !truncated || !modified {
			t.Errorf("%s: truncated=%v modified=%v", name, truncated, modified)
			continue
		}
		if !strings.Contains(resultContent(t, b), "chars from the middle of the output") {
			t.Errorf("%s: marker missing", name)
		}
		if len(resultContent(t, b)) > ToolOutputCharLimit*2 {
			t.Errorf("%s: still %d bytes", name, len(resultContent(t, b)))
		}
	}
}

func TestTruncateToolResult_OtherShapesAndBlocks(t *testing.T) {
	text := claude.NewBlock(claude.TextBlock{Type: "text", Text: "hello"})
	if got, tr, mod := truncateToolResult(text); mod || tr || string(got.Raw) != string(text.Raw) {
		t.Fatal("a non-tool_result block must pass through")
	}
	for _, content := range []string{`42`, `null`, `true`} {
		if _, tr, mod := truncateToolResult(toolResult(content)); mod || tr {
			t.Errorf("content %s is not text-bearing and must be left alone", content)
		}
	}
}

func TestNormalizeTextBlock(t *testing.T) {
	mk := func(s string) claude.Block { return claude.NewBlock(claude.TextBlock{Type: "text", Text: s}) }
	for _, c := range []struct {
		name    string
		in      string
		role    claude.Role
		dropped bool
		want    string
	}{
		{"ANSI and NUL are cleaned", "\x1b[31mhello\x1b[0m\x00", claude.RoleUser, false, "hello"},
		{"empty after cleaning is dropped", "\x1b[0m\x00", claude.RoleUser, true, ""},
		{"whitespace only is dropped", "  \n", claude.RoleUser, true, ""},
		{"hollow dot from the assistant is dropped", ".", claude.RoleAssistant, true, ""},
		{"a dot from the user is kept", ".", claude.RoleUser, false, "."},
		{"clean text keeps the original block", "fine", claude.RoleUser, false, "fine"},
	} {
		b, dropped, _ := normalizeTextBlock(mk(c.in), c.role)
		if dropped != c.dropped {
			t.Errorf("%s: dropped=%v, want %v", c.name, dropped, c.dropped)
			continue
		}
		if !dropped {
			if tb, _ := b.AsText(); tb.Text != c.want {
				t.Errorf("%s: text %q, want %q", c.name, tb.Text, c.want)
			}
		}
	}
}
