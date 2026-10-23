// Package tokens merges superrelay (v1)'s token_estimator.go (627 lines)
// and context_window.go (458 lines) into one package built on the typed
// internal/protocol/claude domain. Per the migration spec (Part 5's
// roadmap), this merge is EXPECTED to produce a real behavioral diff, not
// a zero-diff port: the two v1 files independently reimplemented the same
// per-content-block pricing dispatch (text/thinking/tool_use/tool_result/
// image/document), each carrying its own partial subset of the many
// audit-caught fixes documented in their comments — a textbook case of the
// migration spec's Part 1.2 "divergent duplication" root cause. This
// package's blockcost.go merges that dispatch into ONE function
// (blockCost), called by both of this file's leaf estimators, so a future
// fix only has one call site to land in instead of two that can silently
// drift apart again. See blockcost.go's doc comment for the one
// deliberate, documented behavior change this merge makes (unknown block
// types, such as server_tool_use/web_search_tool_result, which v1's two
// estimators disagreed on pricing as 0 vs. a flat estimate).
package tokens

import (
	"encoding/json"
	"math"
	"unicode/utf8"
)

// WeightedCharTokens computes a weighted character-to-token approximation
// using Anthropic's BPE weight characteristics (ASCII, digits, punctuation,
// non-ASCII/CJK/Cyrillic) — direct port of v1's estimateApproxTokens
// (token_estimator.go). Used for the "realistic" estimate this gateway
// reports to the client (message_start's input_tokens, count_tokens).
func WeightedCharTokens(text string) int {
	if text == "" {
		return 0
	}

	length := utf8.RuneCountInString(text)
	if length == 0 {
		return 0
	}
	if length < 5 {
		return int(math.Ceil(float64(length) / 3.0))
	}

	var regularAscii, digits, symbols, nonASCII, cjk int
	for _, r := range text {
		switch {
		case isCJK(r):
			// GAP-D4-08: CJK characters were falling into the generic
			// ">=0x80" bucket at the same /1.5 weight as any other
			// non-ASCII byte (accented Latin, Cyrillic, symbols) — real
			// CJK text runs closer to 1.0-1.5 tokens/char, not the
			// implied ~0.67, systematically underestimating Chinese/
			// Japanese/Korean text by roughly a third to a half.
			cjk++
		case r >= 0x80:
			nonASCII++
		case r >= '0' && r <= '9':
			digits++
		case (r >= '!' && r <= '/') || (r >= ':' && r <= '@') || (r >= '[' && r <= '`') || (r >= '{' && r <= '~'):
			symbols++
		default:
			regularAscii++
		}
	}

	estimated := int(math.Ceil(
		float64(regularAscii)/4.5 +
			float64(digits)/2.0 +
			float64(symbols)/1.5 +
			float64(nonASCII)/1.5 +
			float64(cjk)/1.25,
	))

	if estimated < 1 {
		return 1
	}
	return estimated
}

// isCJK reports whether r falls in one of the main CJK (Chinese,
// Japanese, Korean) Unicode blocks — Han ideographs (including
// Extension A), Hiragana/Katakana, and Hangul syllables. Not
// exhaustive of every CJK-adjacent block, but covers the overwhelming
// majority of real CJK text.
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK Unified Ideographs Extension A
		return true
	case r >= 0x3040 && r <= 0x30FF: // Hiragana + Katakana
		return true
	case r >= 0xAC00 && r <= 0xD7A3: // Hangul syllables
		return true
	}
	return false
}

// FlatJSONTokens is the pessimistic flat-rate estimate (2 bytes/token) v1's
// context_window.go uses ONLY for the truncation safety check — it
// deliberately ignores WeightedCharTokens' character-class weighting,
// since that can undercount by 2x+ on content that doesn't match its
// prose assumptions (verified in v1: a 280,000-char synthetic payload came
// back as 141,065 real tokens from Google — ~2.0 bytes/token — while the
// weighted estimator said ~62,500). Direct port of v1's flatTokenEstimate,
// taking the already-marshaled bytes directly rather than re-marshaling a
// value, so it's exact for a claude.Block's own Raw bytes (no risk of a
// decoded-and-remarshaled struct silently dropping a field the original
// JSON had).
func FlatJSONTokens(raw []byte) int {
	n := len(raw) / 2
	if n < 1 {
		return 1
	}
	return n
}

// FlatTextTokens applies FlatJSONTokens to s as v1 does when it calls
// flatTokenEstimate(aPlainString) — the string is JSON-marshaled first
// (adding quotes/escaping) before the byte-length/2 floor, matching v1's
// behavior exactly rather than using len(s) directly.
func FlatTextTokens(s string) int {
	b, _ := json.Marshal(s)
	return FlatJSONTokens(b)
}
