// Rule 2 — ToolCallResponseParity. Ported verbatim from superrelay
// (v1)'s cmd/speccheck/rules.go (checkToolCallResponseParity): every
// functionCall in turn N must be answered by exactly one
// functionResponse in turn N+1. Caught live: BUG-R02, a delayed subagent
// tool_result desynchronizing the function-call/response count Gemini
// requires strict adjacency for.
package rules

import "fmt"

func checkToolCallResponseParity(rec CaptureRecord) []Violation {
	body := asMap(rec.snapshotBody("gemini_request"))
	if body == nil {
		return nil
	}
	contents := asArray(body["contents"])
	var out []Violation
	for i := 0; i < len(contents); i++ {
		turn := asMap(contents[i])
		if asString(turn["role"]) != "model" {
			continue
		}
		calls := 0
		for _, p := range asArray(turn["parts"]) {
			if asMap(asMap(p)["functionCall"]) != nil {
				calls++
			}
		}
		if calls == 0 {
			continue
		}
		responses := 0
		if i+1 < len(contents) {
			next := asMap(contents[i+1])
			for _, p := range asArray(next["parts"]) {
				if asMap(asMap(p)["functionResponse"]) != nil {
					responses++
				}
			}
		}
		if responses != calls {
			out = append(out, violation(rec, fmt.Sprintf("contents[%d] (model) has %d functionCall(s) but contents[%d] has %d functionResponse(s)", i, calls, i+1, responses)))
		}
	}
	return out
}

var ToolCallResponseParity = Rule{
	Name: "ToolCallResponseParity",
	Check: func(records []CaptureRecord) []Violation {
		var out []Violation
		for _, rec := range records {
			out = append(out, checkToolCallResponseParity(rec)...)
		}
		return out
	},
}
