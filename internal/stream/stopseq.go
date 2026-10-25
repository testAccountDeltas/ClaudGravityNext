// Part of the stream package — see events.go for the package doc comment.
// Gateway-side stop_sequences matching: Gemini cuts the text itself and
// reports a plain STOP, so a sequence that fired is indistinguishable from
// a natural end. The sequences are therefore NOT forwarded upstream
// (internal/translate/generation.go) and matched here instead.
package stream

import "strings"

// findStopSequence returns the byte offset and text of the earliest stop
// sequence occurring in s; on a tie at the same offset the longer wins.
// seq == "" means no sequence matched.
func (f *FSM) findStopSequence(s string) (idx int, seq string) {
	idx = -1
	for _, cand := range f.stopSequences {
		if cand == "" {
			continue
		}
		i := strings.Index(s, cand)
		if i < 0 {
			continue
		}
		if idx < 0 || i < idx || (i == idx && len(cand) > len(seq)) {
			idx, seq = i, cand
		}
	}
	return idx, seq
}
