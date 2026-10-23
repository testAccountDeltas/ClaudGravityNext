package stream

import (
	"strings"
	"testing"
	"unicode/utf8"

	"claudgravity/internal/tokens"
)

func TestTrimToTokenBudget(t *testing.T) {
	if got, cut := TrimToTokenBudget("short", 100); got != "short" || cut {
		t.Fatalf("fitting text must pass untouched: %q %v", got, cut)
	}
	if got, cut := TrimToTokenBudget("anything", 0); got != "" || !cut {
		t.Fatalf("zero budget: %q %v", got, cut)
	}
	text := strings.Repeat("Привет мир, 你好世界! ", 50)
	got, cut := TrimToTokenBudget(text, 30)
	if !cut || got == "" || !utf8.ValidString(got) {
		t.Fatalf("cut=%v valid=%v len=%d", cut, utf8.ValidString(got), len(got))
	}
	if f := tokens.WeightedCharTokensFrac(got); f > 30 {
		t.Fatalf("trimmed text still over budget: %.2f", f)
	}
	if f := tokens.WeightedCharTokensFrac(text[:len(got)+8]); f <= 30-1 && len(got)+8 < len(text) {
		t.Fatalf("trim is not near-maximal: %.2f", f)
	}
}
