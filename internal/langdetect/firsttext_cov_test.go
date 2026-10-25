package langdetect

import (
	"testing"

	"claudgravity/internal/protocol/claude"
)

func TestCov_FirstTextSkipsNonTextBlocks(t *testing.T) {
	img := claude.NewBlock(claude.ImageBlock{Type: "image"})
	msgs := []claude.Message{{Role: claude.RoleUser, Content: []claude.Block{img, claude.NewBlock(claude.TextBlock{Type: "text", Text: "привет мир"})}}}
	if !IsRussianConversation(msgs) {
		t.Fatal("text after an image block must be found")
	}
	if firstText([]claude.Block{img}) != "" || firstText(nil) != "" {
		t.Fatal("no text -> empty")
	}
}

func TestCov_NoUserTextOrTooFewCyrillic(t *testing.T) {
	if IsRussianConversation(nil) {
		t.Fatal("empty conversation")
	}
	// Latest user message has no text (skipped); falls back to earlier user message.
	msgs := []claude.Message{
		msg(claude.RoleUser, "привет мир"),
		{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.ImageBlock{Type: "image"})}},
	}
	if !IsRussianConversation(msgs) {
		t.Fatal("text-less latest user message must be skipped")
	}
	if IsRussianConversation([]claude.Message{msg(claude.RoleUser, "ab да c")}) {
		t.Fatal("only 2 Cyrillic runes must not count")
	}
}
