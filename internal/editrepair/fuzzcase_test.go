package editrepair

import (
	"fmt"
	"math/rand"
	"strings"
)

// A synthetic, brace-structured source file plus an intended edit, used by the
// property tests: the "truth" is expressed in the file's own indentation and
// distort() turns it into what a model might plausibly send.

type fuzzLine struct {
	depth int
	text  string
}

func (l fuzzLine) render(unit string) string {
	if l.text == "" {
		return ""
	}
	return strings.Repeat(unit, l.depth) + l.text
}

var fuzzCounter int

// genLines emits a random nested structure of statements, "if cN {" openers and
// their closing braces, ending back at depth 0.
func genLines(r *rand.Rand, n int) []fuzzLine {
	var out []fuzzLine
	depth := 0
	for len(out) < n || depth > 0 {
		fuzzCounter++
		switch {
		case depth > 0 && (len(out) >= n || r.Intn(4) == 0):
			depth--
			out = append(out, fuzzLine{depth, "}"})
		case depth < 4 && r.Intn(4) == 0:
			out = append(out, fuzzLine{depth, fmt.Sprintf("if c%d {", fuzzCounter)})
			depth++
		case r.Intn(12) == 0:
			out = append(out, fuzzLine{0, ""})
		default:
			out = append(out, fuzzLine{depth, fmt.Sprintf("stmt%d(a, b)", fuzzCounter)})
		}
	}
	return out
}

func joinLines(ls []fuzzLine, unit string) string {
	parts := make([]string, len(ls))
	for i, l := range ls {
		parts[i] = l.render(unit)
	}
	return strings.Join(parts, "\n")
}

// editBlock returns the replacement for block: one line changed, lines added
// (a plain statement or a whole nested if-block, at a depth that fits), or one
// line removed. Depths stay in the file's frame.
func editBlock(r *rand.Rand, block []fuzzLine) []fuzzLine {
	nb := append([]fuzzLine(nil), block...)
	pos := r.Intn(len(nb))
	for i := pos; i >= 0 && nb[pos].text == ""; i-- { // a blank line has no depth of its own
		nb[pos].depth = nb[i].depth
	}
	if nb[pos].text == "" {
		return nb
	}
	fuzzCounter++
	switch r.Intn(4) {
	case 0: // change one line's text
		if nb[pos].text != "" && nb[pos].text != "}" {
			nb[pos].text = fmt.Sprintf("changed%d(x)", fuzzCounter)
		}
	case 1: // add a sibling statement after pos
		ins := fuzzLine{nb[pos].depth, fmt.Sprintf("added%d()", fuzzCounter)}
		if strings.HasSuffix(nb[pos].text, "{") {
			ins.depth++
		}
		nb = append(nb[:pos+1], append([]fuzzLine{ins}, nb[pos+1:]...)...)
	case 2: // add a nested block after pos
		d := nb[pos].depth
		if strings.HasSuffix(nb[pos].text, "{") {
			d++
		}
		ins := []fuzzLine{{d, fmt.Sprintf("if n%d {", fuzzCounter)}, {d + 1, fmt.Sprintf("body%d()", fuzzCounter)}, {d, "}"}}
		nb = append(nb[:pos+1], append(ins, nb[pos+1:]...)...)
	case 3: // drop a statement
		if len(nb) > 1 && nb[pos].text != "}" && !strings.HasSuffix(nb[pos].text, "{") && nb[pos].text != "" {
			nb = append(nb[:pos], nb[pos+1:]...)
		}
	}
	return nb
}

// distortion models one way a model mangles indentation when quoting a block.
type distortion struct {
	name  string
	apply func(line string) string // applied to every non-empty line of old and new
	first func(line string) string // extra change to line 0 only (may be nil)

	// Cases the repair cannot decide from the text alone are still generated and
	// reported, but only the strict ones fail the test.
	needsStyle    bool   // skip files with no indentation: their style is unknowable
	onlyUnit      string // restrict to files indented with this unit ("" = any)
	skipLine0Edit bool   // skip edits that change or drop the block's first line
	informational bool   // report only; ambiguous by construction

	crlfFile bool   // the file uses CRLF while the model quotes with LF
	suffix   string // the model's old/new end with this (e.g. a newline)
}

func leading(s string) (string, string) {
	t := strings.TrimLeft(s, " \t")
	return s[:len(s)-len(t)], t
}

func distortions(fileUnit string) []distortion {
	tabsToSpaces := func(s string) string {
		ws, rest := leading(s)
		return strings.ReplaceAll(ws, "\t", "    ") + rest
	}
	return []distortion{
		{name: "exact", apply: func(s string) string { return s }},
		{name: "extra-tab", onlyUnit: "\t", apply: func(s string) string { return "\t" + s }},
		{name: "extra-2-tabs", onlyUnit: "\t", apply: func(s string) string { return "\t\t" + s }},
		{name: "extra-4-spaces", onlyUnit: "    ", apply: func(s string) string { return "    " + s }},
		{name: "tabs-to-spaces", onlyUnit: "\t", needsStyle: true, apply: tabsToSpaces},
		{name: "crlf-file", crlfFile: true, apply: func(s string) string { return s }},
		{name: "trailing-newline", suffix: "\n", apply: func(s string) string { return s }},
		{name: "extra-tab+trailing-newline", onlyUnit: "\t", suffix: "\n", apply: func(s string) string { return "\t" + s }},
		{name: "first-line-unindented", skipLine0Edit: true, apply: func(s string) string { return s },
			first: func(s string) string { _, t := leading(s); return t }},
		{name: "extra-tab+first-unindented", onlyUnit: "\t", informational: true, apply: func(s string) string { return "\t" + s },
			first: func(s string) string { _, t := leading(s); return t }},
	}
}

// distort maps a block (old or new, in the file's frame) into the model's frame.
func distort(ls []fuzzLine, unit string, d distortion) string {
	parts := make([]string, len(ls))
	for i, l := range ls {
		s := l.render(unit)
		if s != "" {
			s = d.apply(s)
			if i == 0 && d.first != nil {
				s = d.first(s)
			}
		}
		parts[i] = s
	}
	return strings.Join(parts, "\n")
}
