// Part of the translate package — see translate.go for the package doc
// comment. Isolated per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md's explicit call-out:
// this exact function was the source of GAP-D3-16 (a repeated-merge round
// losing an earlier round's user_note by overwriting instead of
// concatenating), so it gets its own file and its own characterization
// test rather than living buried inside messages.go.
package translate

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// sealFunctionResponseTurn enforces Gemini's "a turn carrying a
// functionResponse must contain ONLY functionResponse parts" rule: if
// parts mixes functionResponse entries with anything else — plain text,
// images — the text is folded into the first functionResponse's
// response.user_note. Images are NOT nested under functionResponse.parts
// — Google's real FunctionResponse message has exactly 3 fields (name,
// response, id) and no "parts", so that previously silently lost any
// screenshot/plot a tool returned. Returns the now-pure functionResponse
// entries, plus any image parts the caller must emit as a separate
// following turn (role "user", pure inlineData — unambiguously valid
// Gemini syntax, unlike the removed functionResponse.parts guess).
func sealFunctionResponseTurn(parts []string) (sealed []string, followUpImages []string) {
	var fnResp, other []string
	for _, p := range parts {
		if gjson.Get(p, "functionResponse").Exists() {
			fnResp = append(fnResp, p)
		} else {
			other = append(other, p)
		}
	}
	if len(fnResp) == 0 || len(other) == 0 {
		return parts, nil
	}

	var note strings.Builder
	for _, p := range other {
		if t := gjson.Get(p, "text"); t.Exists() {
			if note.Len() > 0 {
				note.WriteString("\n")
			}
			note.WriteString(t.String())
		} else if gjson.Get(p, "inlineData").Exists() {
			followUpImages = append(followUpImages, p)
		}
	}

	target := fnResp[0]
	if note.Len() > 0 {
		// A growing combined parts list, as consecutive same-role turns
		// keep merging (see messages.go's own re-sealing of an
		// already-sealed previous turn's parts), can call this again with
		// target already carrying a user_note from an EARLIER merge round
		// — concatenate instead of overwriting, or the first round's text
		// is discarded entirely (GAP-D3-16).
		newNote := note.String()
		if existing := gjson.Get(target, "functionResponse.response.user_note").String(); existing != "" {
			newNote = existing + "\n" + newNote
		}
		target, _ = sjson.Set(target, "functionResponse.response.user_note", newNote)
	}
	fnResp[0] = target
	return fnResp, followUpImages
}
