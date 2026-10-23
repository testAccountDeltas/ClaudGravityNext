// Rule 4 — RequiredResponseHeaders. Ported verbatim from superrelay
// (v1)'s cmd/speccheck/rules.go (checkRequiredResponseHeaders): Claude
// Code's own backoff/countdown logic reads these headers. Caught live:
// writeAnthropicError not setting anthropic-ratelimit-unified-reset on a
// 429 (fixed in v1 commit ea9e5c6).
package rules

import "fmt"

func checkRequiredResponseHeaders(rec CaptureRecord) []Violation {
	headers := rec.snapshotHeaders("claude_response")
	if headers == nil {
		return nil
	}
	body := asMap(rec.snapshotBody("claude_response"))
	status := 200
	if body != nil {
		if t, ok := body["type"].(string); ok && t == "error" {
			status = 0 // unknown status for a bare error body capture; skip
		}
	}
	if status == 0 {
		return nil
	}
	has := func(name string) bool {
		_, ok := headers[name]
		if ok {
			return true
		}
		for k := range headers {
			if equalFoldASCII(k, name) {
				return true
			}
		}
		return false
	}
	var out []Violation
	for _, want := range []string{
		"anthropic-ratelimit-requests-limit",
		"anthropic-ratelimit-requests-remaining",
		"anthropic-ratelimit-tokens-limit",
		"anthropic-ratelimit-tokens-remaining",
	} {
		if !has(want) {
			out = append(out, violation(rec, fmt.Sprintf("response missing expected header %q", want)))
		}
	}
	return out
}

var RequiredResponseHeaders = Rule{
	Name: "RequiredResponseHeaders",
	Check: func(records []CaptureRecord) []Violation {
		var out []Violation
		for _, rec := range records {
			out = append(out, checkRequiredResponseHeaders(rec)...)
		}
		return out
	},
}
