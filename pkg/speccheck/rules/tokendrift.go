// Rules 6 (TokenAccountingSanity) and 12 (TokenDriftWithinBand) — grouped
// in one file per Part 7.6's own note ("правило 6 + 12 — тот же домен,
// один файл").
//
// Rule 6 is ported verbatim from superrelay (v1)'s cmd/speccheck/rules.go
// (checkTokenAccountingSanity): a per-record sanity check that the
// CLAUDE-facing usage numbers the client was told roughly match what
// Gemini actually billed (usageMetadata.promptTokenCount), independent
// of whichever formula internal/tokens used to produce them.
//
// Rule 12 is genuinely NEW — Part 11's DoD criterion 4 and Part 7.6's
// own text describe a live driftRatio measurement
// (estimated/upstreamPromptTokens, target band [1.00, 1.30] for >=95% of
// a day's traffic) that was never actually built anywhere, in v1 or v2,
// before this migration step (confirmed: grep -rn "driftRatio"
// ../superrelay/*.go returns nothing). internal/tokens/drift.go now
// LOGS that ratio live, from inside the gateway, at the one place both
// the gateway's own pre-flight estimate and Gemini's real
// promptTokenCount exist together (see that file's doc comment) — but a
// log line in gateway.log isn't something cmd/speccheck's offline,
// decoupled corpus check can read back out without parsing free-text log
// lines, which is exactly the kind of brittle coupling the rest of this
// migration avoids. This rule instead INDEPENDENTLY RE-DERIVES the same
// ratio straight from the capture: internal/tokens.WeightedRequestTokens
// on the captured claude_request body (the same function
// internal/orchestrate calls pre-flight) against the captured
// gemini_response's own usageMetadata.promptTokenCount — the identical
// formula, computed a second time from durable NDJSON data instead of a
// transient log line, which is also a stronger CI check: it still works
// on corpus files gathered from BEFORE drift.go's logging existed.
package rules

import (
	"encoding/json"
	"fmt"

	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/tokens"
)

// --- Rule 6: TokenAccountingSanity -------------------------------------

func checkTokenAccountingSanity(rec CaptureRecord) []Violation {
	respBody := asMap(rec.snapshotBody("claude_response"))
	geminiBody := asMap(rec.snapshotBody("gemini_response"))
	if respBody == nil || geminiBody == nil {
		return nil
	}
	usage := asMap(respBody["usage"])
	if usage == nil {
		return nil
	}
	claudeIn, _ := usage["input_tokens"].(float64)
	cacheRead, _ := usage["cache_read_input_tokens"].(float64)
	cacheCreate, _ := usage["cache_creation_input_tokens"].(float64)
	claudeTotal := claudeIn + cacheRead + cacheCreate

	usageMeta := asMap(geminiBody["usageMetadata"])
	if usageMeta == nil {
		return nil
	}
	geminiPrompt, ok := usageMeta["promptTokenCount"].(float64)
	if !ok || geminiPrompt == 0 {
		return nil
	}
	delta := claudeTotal - geminiPrompt
	if delta < 0 {
		delta = -delta
	}
	if delta > geminiPrompt*0.10+50 {
		return []Violation{violation(rec, fmt.Sprintf("claude-side input accounting (%.0f) diverges from Gemini's own promptTokenCount (%.0f) by more than 10%%", claudeTotal, geminiPrompt))}
	}
	return nil
}

var TokenAccountingSanity = Rule{
	Name: "TokenAccountingSanity",
	Check: func(records []CaptureRecord) []Violation {
		var out []Violation
		for _, rec := range records {
			out = append(out, checkTokenAccountingSanity(rec)...)
		}
		return out
	},
}

// --- Rule 12: TokenDriftWithinBand -------------------------------------

// driftBandLow/High are the target band Part 6.4/Part 11 DoD #4 define:
// driftRatio := estimated / upstreamPromptTokens should land in
// [1.00, 1.30] for at least 95% of requests.
const (
	driftBandLow     = 1.00
	driftBandHigh    = 1.30
	driftMinFraction = 0.95
)

// upstreamPromptTokens finds Gemini's real promptTokenCount for rec,
// whichever shape the response was captured in: a complete
// non-streaming body, or the LAST chunk of a streamed response that
// reported a non-zero promptTokenCount (usageMetadata is typically only
// present on the final chunk, but scanning from the end costs nothing on
// corpora this small and is robust to either placement).
func upstreamPromptTokens(rec CaptureRecord) (float64, bool) {
	if geminiBody := asMap(rec.snapshotBody("gemini_response")); geminiBody != nil {
		if um := asMap(geminiBody["usageMetadata"]); um != nil {
			if pt, ok := um["promptTokenCount"].(float64); ok && pt > 0 {
				return pt, true
			}
		}
	}
	chunks := rec.streamedChunks()
	for i := len(chunks) - 1; i >= 0; i-- {
		chunk := asMap(chunks[i])
		if chunk == nil {
			continue
		}
		if um := asMap(chunk["usageMetadata"]); um != nil {
			if pt, ok := um["promptTokenCount"].(float64); ok && pt > 0 {
				return pt, true
			}
		}
	}
	return 0, false
}

// estimatedRequestTokens re-derives this gateway's own pre-flight
// estimate for rec's captured claude_request body, using the SAME
// function (internal/tokens.WeightedRequestTokens) internal/orchestrate
// calls live — not a second, independently-written estimator, which
// would reintroduce exactly the "divergent duplication" (Part 1.2) this
// whole migration exists to eliminate.
func estimatedRequestTokens(rec CaptureRecord) (int, bool) {
	body := rec.snapshotBody("claude_request")
	if body == nil {
		return 0, false
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, false
	}
	var req claude.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return 0, false
	}
	return tokens.WeightedRequestTokens(req), true
}

func checkTokenDriftWithinBand(records []CaptureRecord) []Violation {
	type sample struct {
		rec   CaptureRecord
		ratio float64
	}
	var samples []sample
	for _, rec := range records {
		estimated, ok := estimatedRequestTokens(rec)
		if !ok {
			continue
		}
		upstream, ok := upstreamPromptTokens(rec)
		if !ok {
			continue
		}
		samples = append(samples, sample{rec: rec, ratio: float64(estimated) / upstream})
	}
	if len(samples) == 0 {
		return nil
	}

	inBand := 0
	var out []Violation
	for _, s := range samples {
		if s.ratio >= driftBandLow && s.ratio <= driftBandHigh {
			inBand++
		} else {
			out = append(out, violation(s.rec, fmt.Sprintf("driftRatio %.4f outside target band [%.2f, %.2f]", s.ratio, driftBandLow, driftBandHigh)))
		}
	}

	fraction := float64(inBand) / float64(len(samples))
	if fraction < driftMinFraction {
		// The per-sample outliers above are already reported individually
		// for manual follow-up (Part 7.6 #12: "выбросы логируются с rid
		// для ручного разбора, а не тихо усредняются") — this additional
		// summary violation is the AGGREGATE verdict itself, carrying no
		// single rid since it describes the whole corpus.
		out = append(out, Violation{
			RID:    "aggregate",
			Detail: fmt.Sprintf("only %.1f%% of %d request(s) had driftRatio within [%.2f, %.2f], below the required %.0f%%", fraction*100, len(samples), driftBandLow, driftBandHigh, driftMinFraction*100),
		})
	}
	return out
}

var TokenDriftWithinBand = Rule{
	Name:  "TokenDriftWithinBand",
	Check: checkTokenDriftWithinBand,
}
