// Package langdetect picks which language gateway-synthesized strings use
// (the web-search grounding-sources footer, currently) — this text
// bypasses the model entirely (built by the gateway itself after the
// model has already finished, or instead of calling it), so the model has
// no chance to match its own output language to it.
//
// Extracted out of superrelay (v1)'s token_estimator.go, where it lived
// purely by accident of which file someone was editing when they added
// it — it has nothing to do with token counting, which is why it isn't
// part of internal/tokens despite sharing that file's old address.
package langdetect

import "claudgravity/internal/protocol/claude"

// IsRussianConversation checks the latest user-role message in msgs for
// Cyrillic text (3+ Cyrillic runes) — the user's own latest message is
// the freshest signal for "what language is this conversation in right
// now."
func IsRussianConversation(msgs []claude.Message) bool {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != claude.RoleUser {
			continue
		}
		text := firstText(m.Content)
		if text == "" {
			continue
		}
		cyrillic := 0
		for _, r := range text {
			if r >= 0x0400 && r <= 0x04FF {
				cyrillic++
				if cyrillic >= 3 {
					return true
				}
			}
		}
		return false
	}
	return false
}

func firstText(blocks []claude.Block) string {
	for _, b := range blocks {
		if t, ok := b.AsText(); ok {
			return t.Text
		}
	}
	return ""
}
