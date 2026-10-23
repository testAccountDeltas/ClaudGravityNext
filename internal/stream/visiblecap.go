// Part of the stream package — see events.go for the package doc comment.
// Enforcement of the CLIENT's max_tokens on VISIBLE output (text only;
// thinking never counts and tool_use blocks are exempt so a call is never
// cut mid-JSON). Gemini's maxOutputTokens is one shared ceiling for hidden
// thinking AND the answer, so internal/translate widens it by an allowance
// for thinking the model cannot avoid; this is the other half of that
// contract, giving the client the Anthropic semantics back: never more than
// max_tokens of answer, and stop_reason=max_tokens when the cap is hit.
package stream

import (
	"unicode/utf8"

	"claudgravity/internal/tokens"
)

// TrimToTokenBudget returns the longest rune-aligned prefix of text whose
// estimated size (tokens.WeightedCharTokensFrac) does not exceed budget,
// and whether anything was cut. A non-positive budget yields "".
func TrimToTokenBudget(text string, budget float64) (string, bool) {
	if tokens.WeightedCharTokensFrac(text) <= budget {
		return text, false
	}
	if budget <= 0 {
		return "", true
	}
	lo, hi := 0, len(text) // invariant: prefix [:lo] fits, [:hi] does not
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		for mid > lo && !utf8.RuneStart(text[mid]) {
			mid--
		}
		if mid <= lo {
			break
		}
		if tokens.WeightedCharTokensFrac(text[:mid]) <= budget {
			lo = mid
		} else {
			hi = mid
		}
	}
	return text[:lo], true
}

// fitToCap clips one outgoing text delta to what is left of the client's
// max_tokens. hit reports that the cap is now reached. The cap is not
// enforced while upstream's own exact count of visible candidate tokens
// (candidatesTokenCount, which excludes thoughts) proves the answer still
// fits: the estimator is only a fallback and must never cut a legitimate
// answer short.
func (f *FSM) fitToCap(text string) (out string, hit bool) {
	cost := tokens.WeightedCharTokensFrac(text)
	defer func() { f.visibleEst += tokens.WeightedCharTokensFrac(out) }()
	if f.visibleCap <= 0 || (f.candTokens > 0 && f.candTokens <= f.visibleCap) {
		return text, false
	}
	remaining := float64(f.visibleCap) - f.visibleEst
	if cost <= remaining {
		return text, false
	}
	out, _ = TrimToTokenBudget(text, remaining)
	return out, true
}

// clampOutputToCap keeps the reported output_tokens consistent with the
// reported stop_reason: visible tokens never exceed the client's
// max_tokens (thinking is reported on top, as Anthropic bills it). A tool
// call exempts the turn: its arguments are part of candidatesTokenCount.
func (f *FSM) clampOutputToCap(stopReason string) {
	if f.visibleCap <= 0 || f.toolUseOccurred {
		return
	}
	limit := f.visibleCap + f.thinkingTokens
	switch {
	case f.capReached || stopReason == "max_tokens" && f.candTokens > f.visibleCap:
		f.outputTokens = limit
	case f.outputTokens > limit:
		f.outputTokens = limit
	}
}
