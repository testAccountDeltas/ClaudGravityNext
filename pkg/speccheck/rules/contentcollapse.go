// Rule 3 — ContentNotCollapsed. Ported verbatim from superrelay (v1)'s
// cmd/speccheck/rules.go (checkContentNotCollapsed): the exact signature
// of two of v1's own live incidents — a message's content silently
// replaced with the bare "..." sentinel while its neighbors have real
// (redacted-but-present) content. Checked on the CLAUDE-facing request
// leg, since that's where a human would notice the user's own words
// went missing.
package rules

import "fmt"

func checkContentNotCollapsed(rec CaptureRecord) []Violation {
	body := asMap(rec.snapshotBody("claude_request"))
	if body == nil {
		return nil
	}
	msgs := asArray(body["messages"])
	if len(msgs) < 2 {
		return nil
	}
	collapsedCount := 0
	totalCount := 0
	for _, m := range msgs {
		msg := asMap(m)
		blocks := asArray(msg["content"])
		totalCount++
		if len(blocks) == 1 {
			b := asMap(blocks[0])
			if asString(b["type"]) == "text" && asString(b["text"]) == "..." {
				collapsedCount++
			}
		}
	}
	// One or two legitimate "..." sentinels (a genuinely empty turn) are
	// normal and intentional (internal/sanitize's own fallback) — the
	// signature of the actual bug is EVERY turn collapsing at once.
	if totalCount >= 2 && collapsedCount == totalCount {
		return []Violation{violation(rec, fmt.Sprintf("all %d message(s) in this request collapsed to the \"...\" placeholder", totalCount))}
	}
	return nil
}

var ContentNotCollapsed = Rule{
	Name: "ContentNotCollapsed",
	Check: func(records []CaptureRecord) []Violation {
		var out []Violation
		for _, rec := range records {
			out = append(out, checkContentNotCollapsed(rec)...)
		}
		return out
	},
}
