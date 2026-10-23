// Part of the tokens package — see estimate.go for the package doc
// comment. LogDriftRatio is the single point of application (same
// discipline already used for stream.ApplyPrefill/GAP-D1-02 in this
// migration) for the live token-drift measurement
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 6.4/Part 11
// DoD criterion 4 describe but which was never actually built in v1
// (confirmed: no "driftRatio" anywhere in superrelay's source) — this is
// new code informed by the spec's description, not a port.
//
// Both call sites (internal/orchestrate's non-streaming path directly,
// internal/stream's chunk processing for the streaming path) already
// have, at the moment Gemini's real usageMetadata.promptTokenCount first
// becomes available, this gateway's OWN pre-flight estimate
// (tokens.WeightedRequestTokens) for the exact same request sitting
// right next to it in scope — this is the one place both numbers exist
// together before any redaction (speccapture's redaction only touches
// the four captured legs, never these two in-memory ints) could discard
// the unredacted text the estimate was computed from.
package tokens

import "claudgravity/internal/logx"

// LogDriftRatio computes and logs driftRatio := estimated /
// upstreamPromptTokens for one request, keyed by rid only — no
// generative text, matching every other logging/capture decision this
// migration has made about not leaking request content (speccapture's
// own redaction threshold, requestlog's MaskKey, etc.). upstreamPromptTokens
// <= 0 means Gemini never reported a real promptTokenCount for this
// request (e.g. a prompt blocked by promptFeedback before any usage was
// billed) — there is nothing real to compare the estimate against, so
// this is a deliberate no-op rather than a divide-by-zero or a
// misleading ratio of 0.
//
// The target band is [1.00, 1.30] (Part 6.4, Part 11 DoD #4, speccheck
// rule 12 TokenDriftWithinBand) — this function only logs the ratio; the
// out-of-band aggregation across a day of traffic is
// pkg/speccheck/rules/tokendrift.go's job, reading this same log line's
// rid back out of gateway.log is NOT how that rule works (it reads the
// NDJSON capture instead) — see tokendrift.go's doc comment for why a
// separate, independently-computed field in the capture is used rather
// than re-parsing this log line.
func LogDriftRatio(rid uint64, estimated int, upstreamPromptTokens int64) {
	if upstreamPromptTokens <= 0 {
		return
	}
	ratio := float64(estimated) / float64(upstreamPromptTokens)
	// Logs the ratio and rid only — deliberately NOT the raw token counts
	// alongside them, even though they're plain integers rather than
	// generative text: a future reader could otherwise reconstruct a
	// crude proxy for prompt SIZE (request content volume) from gateway.log
	// instead of only the health signal (the ratio) this exists to report.
	logx.LogGateway("[driftRatio] rid=%d ratio=%.4f", rid, ratio)
}
