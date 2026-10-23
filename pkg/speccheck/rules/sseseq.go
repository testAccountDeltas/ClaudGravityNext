// Rule 7 — SSEEventSequence. Ported verbatim from superrelay (v1)'s
// cmd/speccheck/rules.go (checkSSEEventSequence): streaming captures
// must follow the documented Anthropic SSE event sequence. A gap or
// out-of-order index here is exactly what an official SDK's own strict
// parser will reject.
package rules

import "fmt"

func checkSSEEventSequence(rec CaptureRecord) []Violation {
	// Streaming responses are recorded as {"events": [...]} at the top
	// of the claude_response leg (see internal/speccapture's Finish)
	// rather than under "body" — reach for that shape directly.
	legRaw, ok := rec["claude_response"].(map[string]interface{})
	if !ok {
		return nil
	}
	eventsRaw, ok := legRaw["events"].([]interface{})
	if !ok {
		// Non-streaming capture (a plain "body") — nothing for this rule.
		return nil
	}
	if len(eventsRaw) == 0 {
		return nil
	}
	// The panic-watchdog early-200 path (internal/orchestrate's
	// startIdleWatchdog) sends HTTP 200 headers and a keepalive
	// "event: ping" BEFORE message_start, specifically because the real
	// Gemini response hasn't arrived yet — Anthropic's own documented
	// SSE protocol permits periodic pings for exactly this purpose. Skip
	// any leading ping events before checking what the stream's real
	// first event is.
	firstIdx := 0
	for firstIdx < len(eventsRaw) && asString(asMap(eventsRaw[firstIdx])["event"]) == "ping" {
		firstIdx++
	}
	first := "message_start"
	if firstIdx < len(eventsRaw) {
		first = asString(asMap(eventsRaw[firstIdx])["event"])
	}
	last := asString(asMap(eventsRaw[len(eventsRaw)-1])["event"])
	var out []Violation
	if firstIdx < len(eventsRaw) && first != "message_start" && first != "error" {
		out = append(out, violation(rec, fmt.Sprintf("stream started with %q (after %d leading ping event(s)), expected message_start (or error)", first, firstIdx)))
	}
	// Anthropic's own documented SSE protocol allows a stream to
	// terminate with an "error" event instead of the normal
	// message_delta -> message_stop pair when generation fails, and the
	// server closes the connection right after; that's a legitimate
	// terminal state, not a sequencing bug.
	if last != "message_stop" && last != "error" {
		out = append(out, violation(rec, fmt.Sprintf("stream ended with %q, expected message_stop (or error)", last)))
	}
	openBlocks := map[float64]bool{}
	for _, e := range eventsRaw {
		ev := asMap(e)
		name := asString(ev["event"])
		data := asMap(ev["data"])
		switch name {
		case "content_block_start":
			idx, _ := data["index"].(float64)
			if openBlocks[idx] {
				out = append(out, violation(rec, fmt.Sprintf("content_block_start reopened already-open index %v", idx)))
			}
			openBlocks[idx] = true
		case "content_block_stop":
			idx, _ := data["index"].(float64)
			if !openBlocks[idx] {
				out = append(out, violation(rec, fmt.Sprintf("content_block_stop closed index %v that was never opened", idx)))
			}
			delete(openBlocks, idx)
		}
	}
	for idx := range openBlocks {
		out = append(out, violation(rec, fmt.Sprintf("content_block index %v opened but never closed before message_stop", idx)))
	}
	return out
}

var SSEEventSequence = Rule{
	Name: "SSEEventSequence",
	Check: func(records []CaptureRecord) []Violation {
		var out []Violation
		for _, rec := range records {
			out = append(out, checkSSEEventSequence(rec)...)
		}
		return out
	},
}
