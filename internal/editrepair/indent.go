// Part of the editrepair package's port from superrelay (v1)'s
// edit_repair.go — see cwd.go for the split rationale. This file holds
// reapplyLineIndents, split out of fuzzymatch.go purely to stay under
// the Part 4.1 file-size ceiling (the combined file was 552 lines) —
// the function body itself is unchanged from v1.
package editrepair

import "strings"

// stripOnlyIndentMarker is a sentinel findFuzzyLineMatch stores in
// lineIndents[0] instead of "" when its own start-offset shift already
// preserved the file's real indentation as untouched surrounding text —
// new_string's line 0 must have any leading whitespace of its OWN
// stripped (not replaced with anything) so it doesn't stack on top of
// that preserved prefix, but nothing should be prepended here.
const stripOnlyIndentMarker = "\x00strip-only\x00"

// reapplyLineIndents prepends lineIndents[j] onto newStr's line j, for every
// j where lineIndents[j] is non-empty and that line doesn't already start
// with whitespace (the model may have gotten SOME of its new lines right).
// Only applies when newStr has exactly as many lines as lineIndents has
// entries — a differing line count means old_string and new_string don't
// correspond 1:1 (lines were added/removed), and guessing which new line
// maps to which old one would risk indenting the wrong line instead of
// just leaving the ambiguous case alone.
func reapplyLineIndents(newStr string, lineIndents []string, fileLine0Indent string, oldStr string) (string, bool) {
	if strings.TrimSpace(newStr) == "" {
		return newStr, false
	}
	hasAny := false
	for _, ind := range lineIndents {
		if ind != "" {
			hasAny = true
			break
		}
	}
	// An audit caught that a single-line old_string expanding into a
	// multi-line new_string had no information at all to indent lines
	// 1..N with: there's no old_string line 1..N to compare against the
	// file, so lineIndents (length 1) carries nothing for them even when
	// old_string's own line 0 matched the file exactly (lineIndents[0] ==
	// ""). Keep going in that specific case so the fallback below can use
	// fileLine0Indent as a best-effort base for the newly-inserted lines,
	// instead of leaving them flush-left.
	if !hasAny && !(len(lineIndents) == 1 && fileLine0Indent != "" && strings.Contains(newStr, "\n")) {
		return newStr, false
	}
	lines := strings.Split(newStr, "\n")
	// An audit caught that findFuzzyLineMatch strips a trailing newline's
	// resulting empty element from old_string's own line split (giving
	// lineIndents a length matching the REAL line count), but this
	// function's own split never got the same treatment — a completely
	// normal new_string that happens to end in "\n" with the exact same
	// real line count as old_string had ONE MORE element here than
	// lineIndents, so the precise exact-1:1 branch below never fired for
	// what should be its most common, simplest case, falling through to
	// the less-precise heuristic branch instead. Strip it the same way,
	// and remember to restore it when rejoining at the end.
	hadTrailingNewline := false
	if len(lines) > 1 && lines[len(lines)-1] == "" && strings.HasSuffix(newStr, "\n") {
		lines = lines[:len(lines)-1]
		hadTrailingNewline = true
	}
	changed := false

	// Captured before line 0 is potentially rewritten below — the
	// model's OWN relative indentation between line 0 and later lines
	// (e.g. line 0 has none of its own, line 3 has 4 extra spaces of its
	// own) is real signal about nesting depth that a flat "apply one
	// uniform indent to every line" replacement throws away. See its use
	// in the differing-line-count branch below.
	origLine0OwnIndent := lines[0][:len(lines[0])-len(strings.TrimLeft(lines[0], " \t"))]

	// Line 0 is handled the same way regardless of whether the rest of
	// the lines correspond 1:1: lineIndents[0] already holds the exact
	// target indentation whenever anything is missing there (findFuzzyLineMatch
	// zeroes it out itself when its own start-offset shift already fixed
	// it), so there's never a need to guess at a uniform indent for it the
	// way lines 1..N do below.
	if len(lineIndents) > 0 && lineIndents[0] == stripOnlyIndentMarker {
		trimmed := strings.TrimLeft(lines[0], " \t")
		if trimmed != lines[0] {
			lines[0] = trimmed
			changed = true
		}
	} else if len(lineIndents) > 0 && lineIndents[0] != "" {
		trimmed := strings.TrimLeft(lines[0], " \t")
		if lineIndents[0]+trimmed != lines[0] {
			lines[0] = lineIndents[0] + trimmed
			changed = true
		}
	} else if len(lineIndents) > 0 && lineIndents[0] == "" && fileLine0Indent != "" && len(lines) > 0 && lines[0] != "" {
		trimmed := strings.TrimLeft(lines[0], " \t")
		if trimmed == lines[0] { // line 0 had no indent of its own
			lines[0] = fileLine0Indent + trimmed
			changed = true
		}
	}

	if len(lines) == len(lineIndents) {
		// Exact 1:1 correspondence — reapply each line's own detected indent.
		// lineIndents[j] is the file's real indentation whenever it differs
		// from old_string's own line j (findFuzzyLineMatch already filters
		// out lines where they match), so replace whatever leading
		// whitespace new_string's line carries — not just fill in an
		// absent one — to also correct a wrong style/depth (e.g. the model
		// mirrored its own space-indented old_string against a
		// tab-indented file).
		for j, ind := range lineIndents {
			if ind == "" || j == 0 {
				continue // line 0 already handled above
			}
			// GAP-ED-01: a genuinely empty line (lines[j]=="") got
			// padded with indentation whitespace here (ind+"" = ind),
			// since this branch never checked for it the way the
			// differing-line-count branch below already does —
			// breaking Python and any other whitespace-sensitive
			// linter on a real blank line.
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			trimmed := strings.TrimLeft(lines[j], " \t")
			if ind+trimmed == lines[j] {
				continue // already correct
			}
			lines[j] = ind + trimmed
			changed = true
		}
	} else {
		// An audit caught that a differing line count (the model added or
		// removed a line — common when editing, not an edge case) made
		// this bail out entirely, leaving EVERY line past the first
		// flush-left with no indentation at all — worse than doing
		// nothing targeted, since this is the common case, not the rare
		// one. No 1:1 line mapping is possible here, but old_string's own
		// lines overwhelmingly tend to share one indentation depth (a
		// single block/function body) — apply the most common non-empty
		// detected indent uniformly to every new_string line (other than
		// line 0, handled above) that doesn't already have its own
		// leading whitespace, instead of giving up and guaranteeing
		// flush-left on all of them.
		counts := make(map[string]int)
		for j, ind := range lineIndents {
			if j == 0 || ind == "" {
				continue
			}
			counts[ind]++
		}
		var baseIndent string
		best := 0
		for ind, n := range counts {
			if n > best {
				best = n
				baseIndent = ind
			}
		}
		if len(lineIndents) == 1 && fileLine0Indent != "" {
			// old_string was a single line, so there's no real lines-1..N
			// detected indentation at all to pick a baseIndent from
			// (counts is necessarily empty) — this is the ONLY case
			// handled by relative-delta preservation below, specifically
			// because it's the only case with no per-line ground truth to
			// prefer instead. An audit caught that naively using
			// fileLine0Indent as a flat baseIndent here flattens nested
			// structure: expanding "def foo():" into
			// "def foo():\n    return 42" (new_string's own text already
			// correctly shows line 1 four spaces DEEPER than its own line
			// 0 baseline) lost that relative nesting and put both lines
			// at the identical depth, corrupting Python's indentation
			// rules. Layer new_string's own relative indentation (vs. its
			// own line 0) on top of the file's real indentation instead
			// of discarding it.
			for j := range lines {
				if j == 0 {
					continue // line 0 already handled above
				}
				if strings.TrimSpace(lines[j]) == "" {
					continue // don't indent a genuinely blank line
				}
				ownLeadingLen := len(lines[j]) - len(strings.TrimLeft(lines[j], " \t"))
				ownIndent := lines[j][:ownLeadingLen]
				trimmed := lines[j][ownLeadingLen:]
				var target string
				if strings.HasPrefix(ownIndent, origLine0OwnIndent) {
					delta := ownIndent[len(origLine0OwnIndent):]
					target = fileLine0Indent + delta + trimmed
				} else {
					// Own indentation doesn't extend line 0's baseline
					// (e.g. an actual dedent) — a prefix-delta can't
					// express that; best-effort flat fallback.
					target = fileLine0Indent + trimmed
				}
				if target == lines[j] {
					continue // already correct
				}
				lines[j] = target
				changed = true
			}
		} else if baseIndent != "" {
			// An audit caught that applying the SAME flat baseIndent to
			// every line here flattens genuinely nested structure within
			// the replacement itself: old_string spanning a for-loop with
			// an if nested inside it has lines at 3 different real
			// depths, but adding one new line inside that if (changing
			// the line count, which is why we're in this branch at all)
			// collapsed the for-header, the if-header, the body, AND
			// both closing braces onto the SAME single depth — not just
			// at the line-0 boundary (the narrower case fixed above), but
			// throughout the whole block.
			//
			// old_string's own lines (split the same way
			// findFuzzyLineMatch did, to stay index-aligned with
			// lineIndents) already carry the REAL per-line file
			// indentation for any line whose own TEXT is unchanged in
			// new_string. A plain content->indent map collides on
			// duplicate lines (two bare "}" closing braces at DIFFERENT
			// real depths hash to the same key, so the second overwrites
			// the first) — match them in ORDER instead, like a simple
			// diff: walk old_string's lines once, and for each one scan
			// new_string forward from just past the previous match to
			// find its next occurrence, assigning that old line's real
			// indent to that exact position. Lines in new_string with no
			// match at all (walked past without ever being found) are
			// genuinely new insertions with no ground truth, and still
			// fall back to baseIndent.
			oldLines := strings.Split(strings.ReplaceAll(oldStr, "\r\n", "\n"), "\n")
			if (strings.HasSuffix(oldStr, "\n") || strings.HasSuffix(oldStr, "\r")) && len(oldLines) > 1 && oldLines[len(oldLines)-1] == "" {
				oldLines = oldLines[:len(oldLines)-1]
			}
			assignedIndent := make([]string, len(lines))
			assigned := make([]bool, len(lines))
			searchFrom := 1
			for k := 1; k < len(oldLines) && k < len(lineIndents); k++ {
				oldOwnLeadingLen := len(oldLines[k]) - len(strings.TrimLeft(oldLines[k], " \t"))
				oldContent := normalizeLineForMatch(oldLines[k][oldOwnLeadingLen:])
				for j := searchFrom; j < len(lines); j++ {
					if normalizeLineForMatch(strings.TrimLeft(lines[j], " \t")) != oldContent {
						continue
					}
					realIndent := lineIndents[k]
					if realIndent == "" {
						// Nothing was missing for this old line, meaning
						// its own leading whitespace already equals the
						// file's — that own whitespace IS the real indent.
						realIndent = oldLines[k][:oldOwnLeadingLen]
					}
					assignedIndent[j] = realIndent
					assigned[j] = true
					searchFrom = j + 1
					break
				}
			}
			// A genuinely new (unmatched) line most likely sits right next
			// to a matched one in the model's own intended structure (a
			// new statement inserted after doA() is almost certainly a
			// sibling of doA(), not sitting at the enclosing block's
			// average depth) — prefer the nearest PRECEDING matched
			// line's real indent over the generic baseIndent guess when
			// one is available.
			//
			// An audit caught that gluing the nearest preceding anchor's
			// indent on flatly (after stripping the new line's OWN
			// leading whitespace) threw away real signal when the model
			// wrote the new line with its own correct, DEEPER relative
			// indent than that anchor — e.g. inserting a statement one
			// level further nested than the line right before it still
			// landed at the anchor's flat depth, not one level deeper.
			// Snapshot each line's own ORIGINAL leading whitespace before
			// any mutation below, then — same technique already used for
			// the single-old-line case above — compute the new line's
			// indentation delta relative to the anchor's own original
			// whitespace, and layer that delta on top of the anchor's
			// REAL (corrected) indent, instead of discarding it. The
			// anchor starts as line 0 itself (own indent / corrected
			// indent) so even an insertion before the first matched line
			// benefits from this, not just insertions after one.
			originalOwnIndent := make([]string, len(lines))
			for j, l := range lines {
				originalOwnIndent[j] = l[:len(l)-len(strings.TrimLeft(l, " \t"))]
			}
			anchorOwnIndent := origLine0OwnIndent
			anchorRealIndent := fileLine0Indent
			for j := range lines {
				if j == 0 {
					continue // line 0 already handled above
				}
				if assigned[j] {
					anchorOwnIndent = originalOwnIndent[j]
					anchorRealIndent = assignedIndent[j]
				}
				if strings.TrimSpace(lines[j]) == "" {
					continue // don't indent a genuinely blank line
				}
				trimmed := strings.TrimLeft(lines[j], " \t")
				var target string
				switch {
				case assigned[j]:
					target = assignedIndent[j] + trimmed
				case anchorRealIndent != "":
					if strings.HasPrefix(originalOwnIndent[j], anchorOwnIndent) {
						delta := originalOwnIndent[j][len(anchorOwnIndent):]
						// Normalize delta style to match anchorRealIndent (prevent mixing tabs and spaces)
						if strings.Contains(anchorRealIndent, "\t") && !strings.Contains(anchorRealIndent, " ") {
							delta = strings.ReplaceAll(delta, "    ", "\t")
						} else if strings.Contains(anchorRealIndent, " ") && !strings.Contains(anchorRealIndent, "\t") {
							delta = strings.ReplaceAll(delta, "\t", "    ")
						}
						target = anchorRealIndent + delta + trimmed
					} else if strings.HasPrefix(anchorOwnIndent, originalOwnIndent[j]) {
						// Handle de-denting relative to anchor
						dedentLen := len(anchorOwnIndent) - len(originalOwnIndent[j])
						if strings.Contains(anchorRealIndent, "\t") && !strings.Contains(anchorRealIndent, " ") {
							dedentLen = (dedentLen + 3) / 4
						}
						if dedentLen < len(anchorRealIndent) {
							target = anchorRealIndent[:len(anchorRealIndent)-dedentLen] + trimmed
						} else {
							target = trimmed
						}
						anchorOwnIndent = originalOwnIndent[j]
						anchorRealIndent = target[:len(target)-len(trimmed)]
					} else if baseIndent != "" {
						target = baseIndent + trimmed
					} else {
						target = anchorRealIndent + trimmed
					}
				case baseIndent != "":
					target = baseIndent + trimmed
				default:
					target = anchorRealIndent + trimmed
				}
				if target == lines[j] {
					continue // already correct
				}
				lines[j] = target
				changed = true
			}
		}
	}
	if !changed {
		return newStr, false
	}
	result := strings.Join(lines, "\n")
	if hadTrailingNewline {
		result += "\n"
	}
	return result, true
}
