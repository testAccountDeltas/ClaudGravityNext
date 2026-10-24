package tokens

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"claudgravity/internal/protocol/claude"
)

func sysPrompt(raw string) claude.SystemPrompt { return claude.SystemPrompt(raw) }

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestWeightedSystemTokensShapesPriceConsistently(t *testing.T) {
	text := "You are a helpful assistant. Follow the repository conventions carefully."
	want := WeightedCharTokens(text)
	if want == 0 {
		t.Fatal("setup")
	}
	shapes := map[string]string{
		"string":         jsonStr(text),
		"blocks":         `[{"type":"text","text":` + jsonStr(text) + `,"cache_control":{"type":"ephemeral"}}]`,
		"array of strs":  `[` + jsonStr(text) + `]`,
		"blocks no type": `[{"text":` + jsonStr(text) + `}]`,
	}
	for name, raw := range shapes {
		if got := weightedSystemTokens(sysPrompt(raw)); got != want {
			t.Errorf("%s: %d, want %d", name, got, want)
		}
	}
	// multi-piece: the sum of per-piece prices in both array shapes
	a, b := "first block of text", "second block, a bit longer than the first"
	sum := WeightedCharTokens(a) + WeightedCharTokens(b)
	if got := weightedSystemTokens(sysPrompt(`[{"text":` + jsonStr(a) + `},{"text":` + jsonStr(b) + `}]`)); got != sum {
		t.Errorf("blocks sum %d, want %d", got, sum)
	}
	if got := weightedSystemTokens(sysPrompt(`[` + jsonStr(a) + `,` + jsonStr(b) + `]`)); got != sum {
		t.Errorf("strings sum %d, want %d", got, sum)
	}
	for _, empty := range []string{``, `""`, `[]`, `null`, `5`} {
		if got := weightedSystemTokens(sysPrompt(empty)); got != 0 {
			t.Errorf("%q priced %d, want 0", empty, got)
		}
	}
}

func TestEstimateForFitMatchesWeightedAndCountsGrowth(t *testing.T) {
	mk := func(n int) claude.Request {
		var req claude.Request
		raw := `{"model":"m","max_tokens":10,"system":"sys","messages":[{"role":"user","content":` + jsonStr(strings.Repeat("hello world ", n)) + `}]}`
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatal(err)
		}
		return req
	}
	small, big := mk(10), mk(1000)
	es, eb := EstimateForFit(small, Gemini3Profile, "fit-sess"), EstimateForFit(big, Gemini3Profile, "fit-sess")
	if es <= 0 || eb <= es {
		t.Fatalf("estimates must be positive and grow with text: %d, %d", es, eb)
	}
	if es != WeightedRequestTokensFor(small, Gemini3Profile) {
		t.Fatalf("without repeated reads EstimateForFit must equal the plain estimate: %d vs %d", es, WeightedRequestTokensFor(small, Gemini3Profile))
	}
}

func TestEstimateForFitPricesRepeatedReadsAtStub(t *testing.T) {
	content := strings.Repeat("line of file content\\n", 400)
	read := func(id string) string {
		return `{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"Read","input":{"file_path":"/a/b.go"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","content":"` + content + `"}]}`
	}
	var req claude.Request
	raw := `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"go"},` + read("r1") + `,` + read("r2") + `,` + read("r3") + `]}`
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	plain := WeightedRequestTokensFor(req, Gemini3Profile)
	fit := EstimateForFit(req, Gemini3Profile, "fit-dedup-"+t.Name())
	if fit <= 0 || fit > plain {
		t.Fatalf("EstimateForFit %d must be in (0, plain %d]", fit, plain)
	}
}

// Tolerance table instead of hard upstream numbers: the estimator must be
// monotonic in length, nonzero, and never price a CJK/Hangul/kana/emoji
// character below a Latin one.
func TestWeightedCharTokensScriptTable(t *testing.T) {
	cases := []struct{ name, unit string }{
		{"latin", "a"}, {"digits", "7"}, {"han", "字"}, {"hiragana", "あ"}, {"katakana", "カ"},
		{"hangul", "한"}, {"cyrillic", "я"}, {"emoji", "😀"}, {"symbols", "#"},
	}
	perChar := map[string]float64{}
	for _, c := range cases {
		prev := 0
		for _, n := range []int{5, 10, 100, 1000, 5000} {
			s := strings.Repeat(c.unit, n)
			got := WeightedCharTokens(s)
			if got <= 0 {
				t.Errorf("%s x%d priced %d", c.name, n, got)
			}
			if got < prev {
				t.Errorf("%s: not monotonic at %d: %d < %d", c.name, n, got, prev)
			}
			prev = got
			if frac := WeightedCharTokensFrac(s); frac < 0 || (utf8.RuneCountInString(s) >= 5 && int(frac) > got) {
				t.Errorf("%s x%d: frac %v vs tokens %d", c.name, n, frac, got)
			}
		}
		perChar[c.name] = float64(WeightedCharTokens(strings.Repeat(c.unit, 1000))) / 1000
	}
	for _, name := range []string{"han", "hiragana", "katakana", "hangul", "cyrillic", "emoji"} {
		if perChar[name] < perChar["latin"] {
			t.Errorf("%s priced %.3f/char, below latin %.3f", name, perChar[name], perChar["latin"])
		}
	}
	for _, r := range []rune{'字', 'あ', 'カ', '한', 0x3400, 0x9FFF, 0xAC00, 0xD7A3} {
		if !isCJK(r) {
			t.Errorf("isCJK(%U) = false", r)
		}
	}
	for _, r := range []rune{'a', '7', 'я', '😀', 0x4DFF, 0xD7A4, 0x3000} {
		if isCJK(r) {
			t.Errorf("isCJK(%U) = true", r)
		}
	}
	if WeightedCharTokens("") != 0 || WeightedCharTokens("ab") != 1 {
		t.Error("short/empty rules changed")
	}
	// additivity of the fractional price across concatenation
	a, b := "hello 世界 ", "한국어 test 😀"
	if d := WeightedCharTokensFrac(a+b) - WeightedCharTokensFrac(a) - WeightedCharTokensFrac(b); d > 1e-9 || d < -1e-9 {
		t.Errorf("Frac not additive: %v", d)
	}
}
