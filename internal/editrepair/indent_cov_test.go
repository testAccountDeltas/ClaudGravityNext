package editrepair

import (
	"reflect"
	"testing"
)

func TestNormalizeLineForMatch_Typography(t *testing.T) {
	in := "\u201chi\u201d \u2018a\u2019\u00a0\u2013\u2014 x\u200b\ufeff\t\ty  "
	if got, want := normalizeLineForMatch(in), `"hi" 'a' -- x y`; got != want {
		// Whitespace runs collapse; edge trimming is applied last.
		t.Fatalf("got %q want %q", got, want)
	}
	if got := normalizeLineForMatch("  \t "); got != "" {
		t.Fatalf("got %q", got)
	}
	if got := normalizeLineForMatch("\u00ab q \u00bb \u201a\u201b"); got != `" q " ''` {
		t.Fatalf("got %q", got)
	}
}

func TestAlignLines(t *testing.T) {
	cases := []struct {
		name     string
		old, new []string
		limit    int
		want     []int
	}{
		{"identical head and tail",
			[]string{"a", "  b", "c", "}"}, []string{"a", "b", "c", "}"}, 4, []int{-1, 1, 2, 3}},
		{"closing brace binds to its real counterpart",
			[]string{"f {", "x", "}"}, []string{"f {", "y", "}"}, 3, []int{-1, -1, 2}},
		{"middle paired in order",
			[]string{"h", "m1", "m2", "t"}, []string{"h", "m1", "NEW", "m2", "t"}, 4, []int{-1, 1, 3, 4}},
		{"limit larger than old is clamped",
			[]string{"a", "b"}, []string{"a", "b"}, 99, []int{-1, 1}},
		{"limit stops the tail pairing",
			[]string{"a", "b", "c"}, []string{"a", "b", "c"}, 2, []int{-1, 1, -1}},
		{"no counterpart",
			[]string{"a", "b"}, []string{"a", "zzz"}, 2, []int{-1, -1}},
		{"curly quote equals straight",
			[]string{"a", "say \u201cx\u201d"}, []string{"a", `say "x"`}, 2, []int{-1, 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := alignLines(c.old, c.new, c.limit); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}

func TestIndentRepairAmbiguous(t *testing.T) {
	strip := stripOnlyIndentMarker
	cases := []struct {
		name     string
		newStr   string
		indents  []string
		file0    string
		oldStr   string
		ambigous bool
	}{
		{"no indents", "x", nil, "", "x", false},
		{"dropped first line, shallower than prefix", "b()\nc()", []string{strip, ""}, "      ", "    a()\n    b()", true},
		{"dropped first line, within prefix", "b()\nc()", []string{strip, ""}, "  ", "    a()\n    b()", false},
		{"dropped first line, column zero is shallower", "b()\nc()", []string{strip, columnZeroMarker}, "  ", "    a()\n    b()", true},
		{"dropped first line, explicit indent kept", "b()", []string{strip, "    "}, "    ", "    a()\n    b()", false},
		{"not stripped so never ambiguous", "b()\nc()", []string{"  ", ""}, "      ", "    a()\n    b()", false},
		{"one-line expanded with indented extras", "x()\n    y()", []string{strip}, "  ", "x()", true},
		{"one-line expanded, extras flush", "x()\ny()", []string{strip}, "  ", "x()", false},
		{"one-line expanded, only blank extras", "x()\n   \n", []string{strip}, "  ", "x()", false},
		{"one-line expanded but file line 0 flush", "x()\n    y()", []string{strip}, "", "x()", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := indentRepairAmbiguous(c.newStr, c.indents, c.file0, c.oldStr); got != c.ambigous {
				t.Fatalf("got %v want %v", got, c.ambigous)
			}
		})
	}
}

func TestReindentLine0(t *testing.T) {
	strip := stripOnlyIndentMarker
	cases := []struct {
		name    string
		lines   []string
		indents []string
		zero    []bool
		file0   string
		oldStr  string
		want    string
		did     bool
	}{
		{"counterpart takes old line k's indent", []string{"b()", "c()"}, []string{"", "    "}, []bool{false, false}, "", "a()\n    b()", "    b()", true},
		{"counterpart zero flag forces flush", []string{"  b()"}, []string{"", "    "}, []bool{false, true}, "", "a()\n    b()", "b()", true},
		{"counterpart derives indent from old text", []string{"b()"}, []string{"", ""}, []bool{false, false}, "", "a()\n\tb()", "\tb()", true},
		{"counterpart unchanged", []string{"    b()"}, []string{"", "    "}, []bool{false, false}, "", "a()\n    b()", "    b()", false},
		{"strip-only counterpart keeps beyond-prefix part", []string{"b()"}, []string{strip, "      "}, []bool{false, false}, "  ", "  a()\n      b()", "    b()", true},
		{"strip-only counterpart shallower than prefix stays flush", []string{"b()"}, []string{strip, "  "}, []bool{false, false}, "    ", "    a()\n  b()", "b()", false},
		{"strip-only trims line 0", []string{"    x()"}, []string{strip}, []bool{false}, "    ", "x()", "x()", true},
		{"strip-only already flush", []string{"x()"}, []string{strip}, []bool{false}, "    ", "x()", "x()", false},
		{"zero flag trims", []string{"  x()"}, []string{""}, []bool{true}, "", "x()", "x()", true},
		{"zero flag already flush", []string{"x()"}, []string{""}, []bool{true}, "", "x()", "x()", false},
		{"explicit indent applied", []string{"x()"}, []string{"\t\t"}, []bool{false}, "", "x()", "\t\tx()", true},
		{"explicit indent replaces wrong one", []string{"  x()"}, []string{"\t"}, []bool{false}, "", "x()", "\tx()", true},
		{"explicit indent already right", []string{"\tx()"}, []string{"\t"}, []bool{false}, "", "x()", "\tx()", false},
		{"empty indent adopts file line 0 indent", []string{"x()"}, []string{""}, []bool{false}, "  ", "x()", "  x()", true},
		{"empty indent keeps own indent", []string{" x()"}, []string{""}, []bool{false}, "  ", "x()", " x()", false},
		{"empty line 0 untouched", []string{""}, []string{""}, []bool{false}, "  ", "x()", "", false},
		{"no indents at all", []string{"x()"}, nil, nil, "  ", "x()", "x()", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lines := append([]string(nil), c.lines...)
			did := reindentLine0(lines, c.indents, c.zero, c.file0, c.oldStr)
			if did != c.did || lines[0] != c.want {
				t.Fatalf("did=%v line0=%q; want did=%v line0=%q", did, lines[0], c.did, c.want)
			}
		})
	}
}
