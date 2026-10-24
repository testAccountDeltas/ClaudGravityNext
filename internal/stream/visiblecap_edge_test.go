package stream

import (
	"strings"
	"testing"
	"unicode/utf8"

	"claudgravity/internal/reqctx"
	"claudgravity/internal/tokens"
)

func TestFitToCap_CostEqualToRemaining_FitsThenNextIsClipped(t *testing.T) {
	text := "aaaaaaaaa" // 9 plain ASCII chars = exactly 2.0 tokens
	if c := tokens.WeightedCharTokensFrac(text); c != 2 {
		t.Fatalf("fixture assumption broken: %v", c)
	}
	f := &FSM{visibleCap: 2}
	out, hit := f.fitToCap(text)
	if out != text || hit {
		t.Fatalf("cost == remaining must fit untouched: %q %v", out, hit)
	}
	out, hit = f.fitToCap("more")
	if out != "" || !hit {
		t.Fatalf("budget spent: next delta must be dropped and flag the cap: %q %v", out, hit)
	}
}

func TestFitToCap_MultibyteBoundaryStaysValidUTF8(t *testing.T) {
	text := strings.Repeat("Привет, мир! Как дела? ", 20) + "你好世界"
	for cap := int64(1); cap <= 60; cap += 7 {
		f := &FSM{visibleCap: cap}
		out, hit := f.fitToCap(text)
		if !hit || !utf8.ValidString(out) || !strings.HasPrefix(text, out) {
			t.Fatalf("cap %d: hit=%v valid=%v", cap, hit, utf8.ValidString(out))
		}
		if c := tokens.WeightedCharTokensFrac(out); c > float64(cap) {
			t.Fatalf("cap %d: kept %.2f tokens", cap, c)
		}
	}
}

func TestFitToCap_SpreadAcrossDeltasAccumulates(t *testing.T) {
	f := &FSM{visibleCap: 3}
	var got strings.Builder
	hit := false
	for i := 0; i < 20 && !hit; i++ {
		var out string
		out, hit = f.fitToCap("word word ")
		got.WriteString(out)
	}
	if !hit || tokens.WeightedCharTokensFrac(got.String()) > 3 {
		t.Fatalf("hit=%v total=%.2f", hit, tokens.WeightedCharTokensFrac(got.String()))
	}
}

func TestFitToCap_UpstreamCountWithinCapWinsOverEstimate_EvenArrivingLate(t *testing.T) {
	long := strings.Repeat("word ", 100)
	f := &FSM{visibleCap: 5}
	if out, hit := f.fitToCap(long); !hit || out == long {
		t.Fatalf("with no exact count the estimate must clip: hit=%v", hit)
	}
	g := &FSM{visibleCap: 5}
	g.candTokens = 4 // candidatesTokenCount arrives (exact, <= cap) before the text is judged
	if out, hit := g.fitToCap(long); hit || out != long {
		t.Fatalf("an exact count within the cap must never be clipped by the estimator")
	}
	h := &FSM{visibleCap: 5}
	h.candTokens = 500 // exact count over the cap: estimator enforces
	if _, hit := h.fitToCap(long); !hit {
		t.Fatal("exact count over the cap must still be enforced")
	}
}

func TestClampOutputToCap_ThinkingAndStopReasonTable(t *testing.T) {
	cases := []struct {
		name       string
		mk         func() *FSM
		stop       string
		wantOutput int64
	}{
		{"no cap leaves usage alone", func() *FSM { return &FSM{outputTokens: 900} }, "end_turn", 900},
		{"cap reached reports cap plus thinking", func() *FSM { return &FSM{visibleCap: 100, thinkingTokens: 50, capReached: true, outputTokens: 30} }, "max_tokens", 150},
		{"inflated output clamped to cap plus thinking", func() *FSM { return &FSM{visibleCap: 100, thinkingTokens: 50, outputTokens: 400} }, "end_turn", 150},
		{"honest output below the limit untouched", func() *FSM { return &FSM{visibleCap: 100, thinkingTokens: 50, outputTokens: 120} }, "end_turn", 120},
		{"max_tokens with exact count over cap", func() *FSM { return &FSM{visibleCap: 100, candTokens: 180, outputTokens: 180} }, "max_tokens", 100},
		{"tool call exempts the turn", func() *FSM { return &FSM{visibleCap: 100, toolUseOccurred: true, outputTokens: 900} }, "tool_use", 900},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := c.mk()
			f.clampOutputToCap(c.stop)
			if f.outputTokens != c.wantOutput {
				t.Fatalf("output_tokens = %d, want %d", f.outputTokens, c.wantOutput)
			}
		})
	}
}

func TestVisibleCap_PrefillCountsAndStaysAPrefix(t *testing.T) {
	p := &reqctx.Params{ModelName: "claude-opus-4-5", MaxTokens: 6, PrefillText: `{"answer": "`}
	_, body, err := feedText(p, strings.Repeat("lorem ipsum ", 50))
	if err != nil {
		t.Fatal(err)
	}
	got := textOf(body)
	if !strings.HasPrefix(got, p.PrefillText) {
		t.Fatalf("prefill must lead the response: %q", got)
	}
	if r, _ := stopOf(body); r != "max_tokens" {
		t.Fatalf("cap spent on prefill+text must end max_tokens, got %s", r)
	}
	if c := tokens.WeightedCharTokensFrac(got); c > 6 {
		t.Fatalf("visible output %.2f tokens exceeds max_tokens 6", c)
	}
}

func TestVisibleCap_ThinkingNeverCounts(t *testing.T) {
	p := &reqctx.Params{ModelName: "claude-opus-4-5", MaxTokens: 5}
	think := strings.Repeat("deep thought ", 100)
	_, body := feedChunks(p,
		chunk(`{"thought":true,"text":`+q(think)+`}`, ""),
		chunk(`{"text":"short"}`, "STOP"))
	if textOf(body) != "short" {
		t.Fatalf("thinking must not eat the visible cap: %q", textOf(body))
	}
	if r, _ := stopOf(body); r != "end_turn" {
		t.Fatalf("stop = %s", r)
	}
}
