package stream

import (
	"io"
	"strings"
	"testing"

	"claudgravity/internal/reqctx"
)

func stopParams(max int64, seqs ...string) *reqctx.Params {
	return &reqctx.Params{ModelName: "claude-opus-4-5", StopSequences: seqs, MaxTokens: max}
}

func TestStopSeq_MidChunk_CutsAndReports(t *testing.T) {
	_, body, err := feedText(stopParams(0, "5"), "1,2,3,4,5,6,7,8")
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(body); got != "1,2,3,4," {
		t.Fatalf("text = %q", got)
	}
	if r, s := stopOf(body); r != "stop_sequence" || s != "5" {
		t.Fatalf("stop = %q/%q: %s", r, s, body)
	}
}

func TestStopSeq_SplitAcrossThreeChunks(t *testing.T) {
	_, body, err := feedText(stopParams(0, "END_HERE"), "keep this END", "_HE", "RE and the tail")
	if err != nil {
		t.Fatal(err)
	}
	if got := textOf(body); got != "keep this " {
		t.Fatalf("text = %q", got)
	}
	if r, s := stopOf(body); r != "stop_sequence" || s != "END_HERE" {
		t.Fatalf("stop = %q/%q", r, s)
	}
}

func TestStopSeq_NearMissThenRealMatch(t *testing.T) {
	_, body, _ := feedText(stopParams(0, "ABC"), "xxAB", "AB", "Cyy")
	if got := textOf(body); got != "xxAB" {
		t.Fatalf("text = %q", got)
	}
	if _, s := stopOf(body); s != "ABC" {
		t.Fatalf("seq = %q", s)
	}
}

func TestStopSeq_EarliestMatchWinsAcrossSequences(t *testing.T) {
	_, body, _ := feedText(stopParams(0, "zz", "b", "aab"), "aaXbbzz")
	if got := textOf(body); got != "aaX" {
		t.Fatalf("text = %q", got)
	}
	if r, s := stopOf(body); r != "stop_sequence" || s != "b" {
		t.Fatalf("stop = %q/%q", r, s)
	}
}

func TestStopSeq_MatchAtVeryStart_NoTextBlock(t *testing.T) {
	_, body, _ := feedText(stopParams(0, "###"), "### nothing before")
	if textOf(body) != "" || strings.Contains(body, "content_block_start") {
		t.Fatalf("no text block expected: %s", body)
	}
	if r, s := stopOf(body); r != "stop_sequence" || s != "###" {
		t.Fatalf("stop = %q/%q", r, s)
	}
}

func TestStopSeq_NoMatch_EndTurnAndFullTextIncludingHeldTail(t *testing.T) {
	_, body, _ := feedText(stopParams(0, "NOPE", "STOP!"), "hello wor", "ld STO")
	if got := textOf(body); got != "hello world STO" {
		t.Fatalf("held partial prefix must be flushed at the end, got %q", got)
	}
	if r, s := stopOf(body); r != "end_turn" || s != "" {
		t.Fatalf("stop = %q/%q", r, s)
	}
	if !strings.Contains(body, `"stop_sequence":null`) {
		t.Fatalf("stop_sequence must be null: %s", body)
	}
}

func TestStopSeq_EmptySequenceIgnored(t *testing.T) {
	_, body, _ := feedText(stopParams(0, ""), "abc")
	if textOf(body) != "abc" {
		t.Fatalf("text = %q", textOf(body))
	}
}

func TestStopSeq_CapReachedBeforeMatch_MaxTokensWins(t *testing.T) {
	long := strings.Repeat("word ", 200)
	_, body, _ := feedText(stopParams(5, "END"), long+"END")
	if r, s := stopOf(body); r != "max_tokens" || s != "" {
		t.Fatalf("cap hit first, want max_tokens/null, got %q/%q", r, s)
	}
	if len(textOf(body)) >= len(long) {
		t.Fatalf("text must be clipped by the cap: %d", len(textOf(body)))
	}
}

func TestStopSeq_MatchBeforeCap_StopSequenceWins(t *testing.T) {
	_, body, _ := feedText(stopParams(1000, "END"), "short answer END and more")
	if r, s := stopOf(body); r != "stop_sequence" || s != "END" {
		t.Fatalf("stop = %q/%q", r, s)
	}
	if textOf(body) != "short answer " {
		t.Fatalf("text = %q", textOf(body))
	}
}

func TestStopSeq_NothingAfterMessageStop_AndUpstreamNotReadAgain(t *testing.T) {
	rec, w := newHeld()
	f := NewFSM(w, 1, stopParams(0, "5"), &reqctxState)
	first := "data: " + string(chunk(`{"text":"1234567"}`, "")) + "\n\n"
	reads := 0
	r := readerFunc(func(p []byte) (int, error) {
		reads++
		if reads > 1 {
			t.Error("upstream was read after the stop sequence ended the turn")
			return 0, io.EOF
		}
		return copy(p, first), nil
	})
	if err := StreamGeminiToClaude(f, r, nil); err != nil {
		t.Fatal(err)
	}
	names := eventNames(rec.Body.String())
	if names[len(names)-1] != "message_stop" {
		t.Fatalf("events must end at message_stop: %v", names)
	}
}

func TestStopSeq_PrefillIsNotScanned_OnlyGeneratedText(t *testing.T) {
	p := stopParams(0, "5")
	p.PrefillText = `{"n": 5, "m": `
	_, body, _ := feedText(p, "1, 2 5 tail")
	if got := textOf(body); got != `{"n": 5, "m": 1, 2 ` {
		t.Fatalf("a sequence inside the client's own prefill must not fire: %q", got)
	}
	if r, s := stopOf(body); r != "stop_sequence" || s != "5" {
		t.Fatalf("stop = %q/%q", r, s)
	}
}
