package langdetect

import (
	"testing"

	"claudgravity/internal/protocol/claude"
)

func msg(role claude.Role, text string) claude.Message {
	return claude.Message{Role: role, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: text})}}
}

func TestIsRussianConversation_DetectsCyrillic(t *testing.T) {
	msgs := []claude.Message{msg(claude.RoleUser, "привет как дела")}
	if !IsRussianConversation(msgs) {
		t.Fatal("expected Cyrillic text to be detected")
	}
}

func TestIsRussianConversation_UsesLatestUserMessageOnly(t *testing.T) {
	msgs := []claude.Message{
		msg(claude.RoleUser, "привет как дела"),
		msg(claude.RoleAssistant, "hello there"),
		msg(claude.RoleUser, "hello in english now"),
	}
	if IsRussianConversation(msgs) {
		t.Fatal("expected the LATEST user message (English) to win, not an earlier one")
	}
}

func TestIsRussianConversation_FewerThanThreeCyrillicDoesNotCount(t *testing.T) {
	msgs := []claude.Message{msg(claude.RoleUser, "café naïve")} // 2 non-ASCII, non-Cyrillic chars
	if IsRussianConversation(msgs) {
		t.Fatal("expected false for non-Cyrillic non-ASCII text")
	}
}
