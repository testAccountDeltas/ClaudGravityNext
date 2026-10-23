// Rule 1 — RoleAlternation. Ported verbatim from superrelay (v1)'s
// cmd/speccheck/rules.go (checkRoleAlternation): Gemini's contents[]
// must never repeat the same role two turns in a row. Caught live:
// loopguard.go's InjectUserNudge appending a second "user" turn when its
// content-type switch didn't recognize the type SanitizeMessagesAndGraph
// had already normalized the last turn's content to (fixed in v1 commit
// 8ef075f). The capture shape this reads (gemini_request.body.contents)
// is unchanged between v1's speccapture.go and v2's
// internal/speccapture — confirmed by reading both.
package rules

import "fmt"

func checkRoleAlternation(rec CaptureRecord) []Violation {
	body := asMap(rec.snapshotBody("gemini_request"))
	if body == nil {
		return nil
	}
	contents := asArray(body["contents"])
	var out []Violation
	prevRole := ""
	for i, c := range contents {
		turn := asMap(c)
		role := asString(turn["role"])
		if role == "" {
			continue
		}
		if role == prevRole {
			out = append(out, violation(rec, fmt.Sprintf("contents[%d] repeats role %q immediately after contents[%d]", i, role, i-1)))
		}
		prevRole = role
	}
	return out
}

var RoleAlternation = Rule{
	Name: "RoleAlternation",
	Check: func(records []CaptureRecord) []Violation {
		var out []Violation
		for _, rec := range records {
			out = append(out, checkRoleAlternation(rec)...)
		}
		return out
	},
}
