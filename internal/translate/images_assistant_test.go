package translate

import (
	"strings"
	"testing"
)

// In a model-role turn Gemini rejects inlineData, so binary attachments are
// replaced by a text placeholder while textual ones pass through.
func TestAssistantAttachments(t *testing.T) {
	for _, c := range []struct {
		name, block string
		want        []string
	}{
		{"url image", `{"type":"image","source":{"type":"url","url":"https://x.example/a.png"}}`, []string{"[Assistant output image: url]"}},
		{"binary image", `{"type":"image","source":{"type":"base64","media_type":"image/jpeg; charset=x","data":"AAAA"}}`, []string{"[Assistant output image: image/jpeg]"}},
		{"image without media type", `{"type":"image","source":{"type":"base64","data":"AAAA"}}`, []string{"[Assistant output image: image/png]"}},
		{"pdf document", `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"AAAA"}}`, []string{"[Assistant output document: application/pdf]"}},
		{"document without media type", `{"type":"document","source":{"type":"base64","data":"AAAA"}}`, []string{"[Assistant output document: application/pdf]"}},
		{"text document stays text", `{"type":"document","title":"T","context":"C","source":{"type":"text","data":"hello doc"}}`, []string{`[Attached Document: "T" - Context: C]`, "hello doc"}},
		{"context only label", `{"type":"document","context":"only ctx","source":{"type":"text","data":"x"}}`, []string{"[Attached Document - Context: only ctx]", "x"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := convertMsgs(t, "assistant", `[`+c.block+`]`)
			if len(got) != len(c.want) {
				t.Fatalf("parts = %q, want %q", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("part %d = %q, want %q", i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestAssistantAndUserUrlAttachments(t *testing.T) {
	got := convertMsgs(t, "assistant", `[{"type":"document","source":{"type":"url","url":"https://x.example/d.pdf"}}]`)
	if len(got) != 1 || !strings.Contains(got[0], "[Assistant output document: url]") {
		t.Fatalf("assistant url document: %q", got)
	}
	got = convertMsgs(t, "user", `[{"type":"image","source":{"type":"url","url":"http://127.0.0.1:1/a.png"}}]`)
	if len(got) != 1 || !strings.Contains(got[0], "could not fetch") {
		t.Fatalf("unfetchable user url: %q", got)
	}
}

func TestContentSourceDocument(t *testing.T) {
	got := convertMsgs(t, "user", `[{"type":"document","source":{"type":"content","content":[{"type":"text","text":"a "},"b"]}},`+
		`{"type":"document","source":{"type":"content","content":"whole"}}]`)
	joined := strings.Join(got, "|")
	if !strings.Contains(joined, "a b") || !strings.Contains(joined, "whole") {
		t.Fatalf("parts = %q", got)
	}
}
