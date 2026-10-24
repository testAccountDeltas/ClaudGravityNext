package editrepair

import "strings"

// reindentLine0 fixes new_string's first line in place and reports whether it
// changed. Line 0 is handled apart from the rest: lineIndents[0] already holds
// the exact target indentation whenever anything is missing there (the matcher
// zeroes it itself when its own start-offset shift already fixed it), so no
// uniform guess is needed the way it is for lines 1..N.
func reindentLine0(lines, lineIndents []string, zero []bool, fileLine0Indent, oldStr string) (did bool) {
	// new_string may have dropped old_string's first line, making its line 0
	// really old line k: then k's real indent applies, not line 0's.
	if k, ok := line0Counterpart(oldStr, lines[0], len(lineIndents)); ok {
		ind, oldK := lineIndents[k], strings.Split(strings.ReplaceAll(oldStr, "\r\n", "\n"), "\n")[k]
		if zero[k] {
			ind = ""
		} else if ind == "" {
			ind = oldK[:len(oldK)-len(strings.TrimLeft(oldK, " \t"))]
		}
		if lineIndents[0] == stripOnlyIndentMarker {
			// The span starts after line 0's real indent, which stays in the
			// file as prefix: only the part beyond it belongs in new_string.
			if !strings.HasPrefix(ind, fileLine0Indent) {
				ind = fileLine0Indent // cannot express a shallower line; leave it flush
			}
			ind = ind[len(fileLine0Indent):]
		}
		if target := ind + strings.TrimLeft(lines[0], " \t"); target != lines[0] {
			lines[0] = target
			did = true
		}
	} else if len(lineIndents) > 0 && lineIndents[0] == stripOnlyIndentMarker {
		trimmed := strings.TrimLeft(lines[0], " \t")
		if trimmed != lines[0] {
			lines[0] = trimmed
			did = true
		}
	} else if len(lineIndents) > 0 && zero[0] {
		if trimmed := strings.TrimLeft(lines[0], " \t"); trimmed != lines[0] {
			lines[0] = trimmed
			did = true
		}
	} else if len(lineIndents) > 0 && lineIndents[0] != "" {
		trimmed := strings.TrimLeft(lines[0], " \t")
		if lineIndents[0]+trimmed != lines[0] {
			lines[0] = lineIndents[0] + trimmed
			did = true
		}
	} else if len(lineIndents) > 0 && lineIndents[0] == "" && fileLine0Indent != "" && len(lines) > 0 && lines[0] != "" {
		trimmed := strings.TrimLeft(lines[0], " \t")
		if trimmed == lines[0] { // line 0 had no indent of its own
			lines[0] = fileLine0Indent + trimmed
			did = true
		}
	}
	return did
}
