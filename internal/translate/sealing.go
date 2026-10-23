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

// sealFunctionResponseTurn enforces Gemini's rule that text beside a
// functionResponse is not allowed: plain text parts are folded into the
// first functionResponse's response.user_note. Image/document inlineData
// parts (a tool_result's screenshots/plots) stay in the SAME user turn,
// after the functionResponse parts — Google's FunctionResponse message has
// no "parts" field to nest them in, and a fabricated model turn between
// the result and the images (the previous design) invented assistant
// speech and a second user turn. Idempotent across repeated merges.
func sealFunctionResponseTurn(parts []string) []string {
	var fnResp, other []string
	for _, p := range parts {
		if gjson.Get(p, "functionResponse").Exists() {
			fnResp = append(fnResp, p)
		} else {
			other = append(other, p)
		}
	}
	if len(fnResp) == 0 || len(other) == 0 {
		return parts
	}

	var note strings.Builder
	var images []string
	for _, p := range other {
		if t := gjson.Get(p, "text"); t.Exists() {
			if note.Len() > 0 {
				note.WriteString("\n")
			}
			note.WriteString(t.String())
		} else if gjson.Get(p, "inlineData").Exists() {
			images = append(images, p)
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
	return append(fnResp, images...)
}
