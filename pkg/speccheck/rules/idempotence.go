// Rule 10 — SanitizeIdempotenceOnCapturedTraffic. NEW rule, built from
// Part 7.6 #10's own description: "прогон sanitize.Clean дважды на
// каждой захваченной реальной сессии из request_captures/, не только на
// 50 характеризационных фикстурах — расширяет 7.3 на весь живой корпус
// постоянно." internal/sanitize's own package doc comment already
// explains why this should hold by construction (its Clean/
// SanitizeMessagesAndGraph never mutates in place and never reads its
// own prior output as anything but an ordinary fresh input) — this rule
// is the corpus-wide, continuously-run proof of that claim against real
// traffic shapes a hand-picked 50-fixture characterization set might not
// happen to include.
package rules

import (
	"bytes"
	"encoding/json"
	"fmt"

	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/sanitize"
)

func checkSanitizeIdempotenceOnCapturedTraffic(rec CaptureRecord) []Violation {
	body := rec.snapshotBody("claude_request")
	if body == nil {
		return nil
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil
	}
	var req claude.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil
	}
	if len(req.Messages) == 0 {
		return nil
	}

	first := sanitize.SanitizeMessagesAndGraph(req)
	second := sanitize.SanitizeMessagesAndGraph(first.Request)

	firstJSON, err1 := json.Marshal(first.Request)
	secondJSON, err2 := json.Marshal(second.Request)
	if err1 != nil || err2 != nil {
		return nil
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		return []Violation{violation(rec, fmt.Sprintf(
			"sanitize.SanitizeMessagesAndGraph(SanitizeMessagesAndGraph(x).Request) != SanitizeMessagesAndGraph(x).Request on real captured traffic — idempotence broken (first pass %d bytes, second pass %d bytes)",
			len(firstJSON), len(secondJSON)))}
	}
	return nil
}

var SanitizeIdempotenceOnCapturedTraffic = Rule{
	Name: "SanitizeIdempotenceOnCapturedTraffic",
	Check: func(records []CaptureRecord) []Violation {
		var out []Violation
		for _, rec := range records {
			out = append(out, checkSanitizeIdempotenceOnCapturedTraffic(rec)...)
		}
		return out
	},
}
