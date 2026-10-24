package editrepair

import "strings"

// indentStyleOf reports how a file indents: "\t" when every indented line starts
// with a tab, "    " when every one starts with spaces in multiples of four, ""
// when it is mixed, unindented or anything else. Only a file with one clear
// style is allowed to overrule the model's own whitespace.
func indentStyleOf(content string) string {
	tabs, spaces, odd := 0, 0, false
	for _, l := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(l, "\t"):
			tabs++
		case strings.HasPrefix(l, " ") && strings.TrimSpace(l) != "":
			spaces++
			if n := len(l) - len(strings.TrimLeft(l, " ")); n%4 != 0 {
				odd = true
			}
		}
	}
	switch {
	case tabs > 0 && spaces == 0:
		return "\t"
	case spaces > 0 && tabs == 0 && !odd:
		return "    "
	}
	return ""
}

// matchIndentStyle converts new_string's indentation to the file's style when
// the model used the other one throughout (every indented line led by spaces
// in a tab file, or by tabs in a four-space file) and no line already uses the
// file's own style. Mixed or already-correct text is returned unchanged.
func matchIndentStyle(fileContent, newStr string) (string, bool) {
	style := indentStyleOf(fileContent)
	if style == "" {
		return newStr, false
	}
	other := "    "
	if style == "    " {
		other = "\t"
	}
	lines := strings.Split(newStr, "\n")
	seenOther := false
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		lead := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		if lead == "" {
			continue
		}
		if strings.Contains(lead, style) && !strings.Contains(lead, other) {
			return newStr, false // already uses the file's style somewhere
		}
		if style == "\t" && (strings.Contains(lead, "\t") || len(lead)%4 != 0) {
			return newStr, false
		}
		if style == "    " && strings.Contains(lead, " ") {
			return newStr, false
		}
		seenOther = true
	}
	if !seenOther {
		return newStr, false
	}
	for i, l := range lines {
		lead := l[:len(l)-len(strings.TrimLeft(l, " \t"))]
		if lead != "" && strings.TrimSpace(l) != "" {
			lines[i] = strings.ReplaceAll(lead, other, style) + l[len(lead):]
		}
	}
	return strings.Join(lines, "\n"), true
}

// matchLineEndings makes new_string use the file's line endings when the file
// is uniformly CRLF or uniformly LF; a file mixing both is left to the caller's
// segment-level logic.
func matchLineEndings(fileContent, newStr string) (string, bool) {
	crlf := strings.Count(fileContent, "\r\n")
	lf := strings.Count(fileContent, "\n")
	switch {
	case crlf > 0 && crlf == lf:
		out := strings.ReplaceAll(strings.ReplaceAll(newStr, "\r\n", "\n"), "\n", "\r\n")
		return out, out != newStr
	case crlf == 0 && lf > 0 && strings.Contains(newStr, "\r\n"):
		return strings.ReplaceAll(newStr, "\r\n", "\n"), true
	}
	return newStr, false
}
