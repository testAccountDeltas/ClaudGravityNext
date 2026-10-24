package editrepair

import "strings"

// indentRepairAmbiguous reports cases where the text alone cannot say how
// new_string should be indented. Guessing there lands a wrong edit silently;
// refusing makes Edit fail with "string not found", so the model re-reads the
// file and retries with exact text. Only called on the fuzzy-repair path, where
// old_string did not match the file as written.
func indentRepairAmbiguous(newStr string, lineIndents []string, fileLine0Indent, oldStr string) bool {
	if len(lineIndents) == 0 {
		return false
	}
	newLines := strings.Split(newStr, "\n")
	stripped := lineIndents[0] == stripOnlyIndentMarker

	// The model dropped old_string's first line and quoted what is now first
	// without indentation, but that line sits shallower than the preserved
	// prefix of the old first line: the span cannot express it.
	if k, ok := line0Counterpart(oldStr, newLines[0], len(lineIndents)); ok && stripped {
		ind := lineIndents[k]
		if ind == columnZeroMarker {
			ind = ""
		} else if ind == "" {
			oldK := strings.Split(strings.ReplaceAll(oldStr, "\r\n", "\n"), "\n")[k]
			ind = oldK[:len(oldK)-len(strings.TrimLeft(oldK, " \t"))]
		}
		if !strings.HasPrefix(ind, fileLine0Indent) {
			return true
		}
	}

	// A one-line old_string quoted without its indentation, expanded into
	// several lines that carry indentation of their own: there is no second
	// matched line to show which frame the model wrote them in.
	if len(lineIndents) == 1 && stripped && fileLine0Indent != "" {
		for _, l := range newLines[1:] {
			if strings.TrimSpace(l) != "" && l[0] != '\n' && strings.TrimLeft(l, " \t") != l {
				return true
			}
		}
	}
	return false
}
