// Part of the stream package — see events.go for the package doc
// comment. ApplyPrefill is the single point of prefill application the
// migration spec calls for (Part 6.3), fixing GAP-D1-02 structurally
// rather than by a test alone: v1 inlined this same three-line check
// directly inside HandleText (sse.go), and the non-streaming path
// (server.go's executeNonStreaming) carried its OWN separate copy — a
// Round 15 audit caught the streaming path's copy had been added later
// and never actually wired in correctly, so a streaming response to an
// assistant-prefill request continued the prefilled string without ever
// echoing the prefix itself, handing the client a truncated/invalid JSON
// prefix. Once internal/orchestrate exists, both the streaming (fsm.go's
// HandleText) and non-streaming paths call this exact same method instead
// of each carrying their own copy.
package stream

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ApplyPrefill prepends f's prefill text to text exactly once (on the
// first real text delta of the turn) and returns the (possibly modified)
// text. A no-op once already applied, or if text already happens to
// start with the prefill (Gemini occasionally echoes it back itself).
func (f *FSM) ApplyPrefill(text string) string {
	return PrependPrefillOnce(f.prefillText, text, &f.prefillEmitted)
}

// PrependPrefillOnce is ApplyPrefill's underlying pure logic, exported so
// internal/orchestrate's non-streaming path (which builds one complete
// response, not a stream, so it has no FSM/SafeSSEWriter to call the
// method on) applies the identical prefill rule instead of carrying its
// own copy — the exact duplication that caused GAP-D1-02 in the first
// place. emitted is a one-shot guard owned by the caller; non-streaming
// passes a throwaway local since its response is built in one pass and
// the rule only ever needs to apply once regardless.
func PrependPrefillOnce(prefillText, text string, emitted *bool) string {
	if *emitted {
		return text
	}
	*emitted = true
	if prefillText == "" {
		return text
	}
	if text == "" {
		return prefillText
	}
	if strings.HasPrefix(text, prefillText) {
		return text
	}
	// GAP-D1-04 & GAP-V2-04 & GAP-05: if text is a substantial in-progress echo of prefillText
	// (at least 3 bytes and ending at a boundary, e.g. "```" or "```json"), return the partial
	// echo untouched so subsequent chunks complete it without duplication.
	// A 1-2 byte accidental prefix match (e.g. "{" or "H") must NOT be treated as an echo,
	// because if the model diverged after 1 character, the rest of prefillText would be permanently lost.
	if len(text) >= 3 && strings.HasPrefix(prefillText, text) {
		rLast, _ := utf8.DecodeLastRuneInString(text)
		if unicode.IsSpace(rLast) || unicode.IsPunct(rLast) || (len(text) >= 3 && strings.HasPrefix(text, "```")) {
			return text
		}
	}
	// If the model began its response with a phrase that partially overlaps
	// with the start of prefillText before diverging (e.g. prefill "Here is the summary:"
	// and continuation "Here is what I found"), strip the overlapping prefix from text
	// to avoid duplicate words while guaranteeing the full prefillText is prepended.
	//
	// Match rune-by-rune so multi-byte UTF-8 sequences are never split mid-rune,
	// and ensure the overlap ends at a word boundary (whitespace/punctuation) to avoid
	// truncating words that merely share a leading prefix (e.g. "The" in "There").
	type runePos struct {
		r          rune
		byteOffset int
	}

	var matched []runePos
	pRest := prefillText
	tRest := text
	offset := 0
	for len(pRest) > 0 && len(tRest) > 0 {
		r1, size1 := utf8.DecodeRuneInString(pRest)
		r2, size2 := utf8.DecodeRuneInString(tRest)
		if r1 != r2 {
			break
		}
		matched = append(matched, runePos{r: r1, byteOffset: offset})
		offset += size1
		pRest = pRest[size1:]
		tRest = tRest[size2:]
	}

	if len(matched) == 0 {
		return prefillText + text
	}

	isBoundary := func(byteLen int) bool {
		if byteLen <= 0 {
			return true
		}
		rBefore, _ := utf8.DecodeLastRuneInString(text[:byteLen])
		if unicode.IsSpace(rBefore) || unicode.IsPunct(rBefore) {
			return true
		}
		if byteLen < len(text) {
			rAfterText, _ := utf8.DecodeRuneInString(text[byteLen:])
			if unicode.IsSpace(rAfterText) || unicode.IsPunct(rAfterText) {
				if byteLen < len(prefillText) {
					rAfterPrefill, _ := utf8.DecodeRuneInString(prefillText[byteLen:])
					if unicode.IsSpace(rAfterPrefill) || unicode.IsPunct(rAfterPrefill) {
						return true
					}
				}
			}
		}
		return false
	}

	overlap := offset
	if !isBoundary(overlap) {
		found := false
		for i := len(matched) - 1; i >= 0; i-- {
			candidate := matched[i].byteOffset + utf8.RuneLen(matched[i].r)
			if isBoundary(candidate) {
				overlap = candidate
				found = true
				break
			}
		}
		if !found {
			overlap = 0
		}
	}

	if overlap > 0 && (overlap == len(text) || (overlap < len(text) && utf8.RuneStart(text[overlap]))) {
		return prefillText + text[overlap:]
	}
	return prefillText + text
}
