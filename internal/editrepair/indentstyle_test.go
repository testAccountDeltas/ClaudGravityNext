package editrepair

import (
	"reflect"
	"testing"
)

func TestIndentStyleOf(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"tabs", "a\n\tb\n\t\tc\n", "\t"},
		{"four spaces", "a\n    b\n        c\n", "    "},
		{"mixed", "a\n\tb\n    c\n", ""},
		{"two-space unit", "a\n  b\n", ""},
		{"no indentation", "a\nb\n", ""},
		{"blank space-only lines ignored", "a\n   \n\tb\n", "\t"},
	} {
		if got := indentStyleOf(c.in); got != c.want {
			t.Errorf("%s: indentStyleOf = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestMatchIndentStyle(t *testing.T) {
	tabFile, spaceFile := "x {\n\ty\n}\n", "x {\n    y\n}\n"
	for _, c := range []struct {
		name, file, in, want string
		changed              bool
	}{
		{"spaces to tabs", tabFile, "if a {\n    b()\n        c()\n}", "if a {\n\tb()\n\t\tc()\n}", true},
		{"tabs to spaces", spaceFile, "if a {\n\tb()\n}", "if a {\n    b()\n}", true},
		{"already tabs", tabFile, "if a {\n\tb()\n}", "if a {\n\tb()\n}", false},
		{"mixed new_string left alone", tabFile, "if a {\n\tb()\n    c()\n}", "if a {\n\tb()\n    c()\n}", false},
		{"odd space width left alone", tabFile, "if a {\n  b()\n}", "if a {\n  b()\n}", false},
		{"unknown file style", "a\nb\n", "if a {\n    b()\n}", "if a {\n    b()\n}", false},
	} {
		got, ok := matchIndentStyle(c.file, c.in)
		if got != c.want || ok != c.changed {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, got, ok, c.want, c.changed)
		}
	}
}

func TestMatchLineEndings(t *testing.T) {
	for _, c := range []struct {
		name, file, in, want string
		changed              bool
	}{
		{"LF into CRLF file", "a\r\nb\r\n", "x\ny", "x\r\ny", true},
		{"CRLF into LF file", "a\nb\n", "x\r\ny", "x\ny", true},
		{"already CRLF", "a\r\nb\r\n", "x\r\ny", "x\r\ny", false},
		{"mixed file untouched", "a\r\nb\n", "x\ny", "x\ny", false},
		{"single line", "a\r\nb\r\n", "x", "x", false},
	} {
		got, ok := matchLineEndings(c.file, c.in)
		if got != c.want || ok != c.changed {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, got, ok, c.want, c.changed)
		}
	}
}

func TestAlignLinesBindsRepeatedLinesAtTheEnds(t *testing.T) {
	old := []string{"a()", "}", "b()", "}"}
	// The model inserted its own "}" before the real closing brace.
	now := []string{"a()", "}", "b()", "if x {", "y()", "}", "}"}
	got := alignLines(old, now, len(old))
	if want := []int{-1, 1, 2, 6}; !reflect.DeepEqual(got, want) {
		t.Fatalf("alignLines = %v, want %v (last brace must bind to the last)", got, want)
	}
}

func TestLine0Counterpart(t *testing.T) {
	old := "first()\n\t}\nthird()"
	if k, ok := line0Counterpart(old, "}", 3); !ok || k != 1 {
		t.Errorf("dropped first line: got (%d,%v), want (1,true)", k, ok)
	}
	if _, ok := line0Counterpart(old, "first()", 3); ok {
		t.Error("unchanged line 0 must not be redirected")
	}
	if _, ok := line0Counterpart(old, "unrelated()", 3); ok {
		t.Error("a genuinely new line 0 must not be redirected")
	}
}
