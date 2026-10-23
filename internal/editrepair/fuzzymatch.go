// Part of the editrepair package's port from superrelay (v1)'s
// edit_repair.go — see cwd.go for the split rationale. This file holds
// the fuzzy line-matching/indent-reapplication engine
// (findFuzzyLineMatch/reapplyLineIndents/normalizeLineForMatch) —
// the core whitespace-insensitive matching logic RepairEditToolCall
// (edit.go) depends on.
package editrepair

import (
	"strings"
	"unicode"
)

// normalizeLineForMatch collapses all whitespace (spaces, tabs) into a single space,
// normalizes typographical/curly quotes and dashes, and trims edges.
func normalizeLineForMatch(s string) string {
	var b strings.Builder
	inSpace := false
	for _, r := range s {
		switch r {
		case '“', '”', '«', '»': // Left/right double curly quotes, guillemets
			r = '"'
		case '‘', '’', '‚', '‛': // Left/right single curly quotes
			r = '\''
		case '–', '—': // En-dash, em-dash
			r = '-'
		case 0x00A0: // Non-breaking space
			r = ' '
		case 0x200B, 0xFEFF: // Zero-width space, BOM
			continue
		}
		if unicode.IsSpace(r) {
			if !inSpace {
				b.WriteByte(' ')
				inSpace = true
			}
		} else {
			b.WriteRune(r)
			inSpace = false
		}
	}
	return strings.TrimSpace(b.String())
}

// fileLineInfo splits bytes into lines while preserving their exact line endings
type fileLineInfo struct {
	text   string // original raw line including ending
	clean  string // line without \r and \n
	norm   string // whitespace normalized
	offset int    // byte offset in file
}

// findFuzzyLineMatch returns (the real file content matching oldStr,
// per-line leading-whitespace that was stripped from the comparison and
// must be re-applied to new_string if the caller doesn't want to lose it,
// ok). lineIndents has exactly as many entries as oldStr has lines; most
// are "" (that line's own indentation already matched).
func findFuzzyLineMatch(fileContent, oldStr string) (string, []string, string, bool) {
	// Parse model lines
	rawOldLines := strings.Split(strings.ReplaceAll(oldStr, "\r\n", "\n"), "\n")
	hasTrailingNewline := strings.HasSuffix(oldStr, "\n") || strings.HasSuffix(oldStr, "\r")
	if hasTrailingNewline && len(rawOldLines) > 1 && rawOldLines[len(rawOldLines)-1] == "" {
		rawOldLines = rawOldLines[:len(rawOldLines)-1]
	}
	if len(rawOldLines) == 0 {
		return "", nil, "", false
	}

	var normOldLines []string
	for _, l := range rawOldLines {
		normOldLines = append(normOldLines, normalizeLineForMatch(l))
	}

	// Parse file lines and track byte offsets
	var fileLines []fileLineInfo
	offset := 0
	rem := fileContent
	for len(rem) > 0 {
		idx := strings.IndexAny(rem, "\r\n")
		var rawLine string
		var cleanLine string
		if idx == -1 {
			rawLine = rem
			cleanLine = rem
			rem = ""
		} else {
			if idx+1 < len(rem) && rem[idx] == '\r' && rem[idx+1] == '\n' {
				rawLine = rem[:idx+2]
				cleanLine = rem[:idx]
				rem = rem[idx+2:]
			} else {
				rawLine = rem[:idx+1]
				cleanLine = rem[:idx]
				rem = rem[idx+1:]
			}
		}
		fileLines = append(fileLines, fileLineInfo{
			text:   rawLine,
			clean:  cleanLine,
			norm:   normalizeLineForMatch(cleanLine),
			offset: offset,
		})
		offset += len(rawLine)
	}

	mLen := len(normOldLines)
	if len(fileLines) < mLen {
		return "", nil, "", false
	}

	// Sliding window scan
	var matchIndices []int
	for i := 0; i <= len(fileLines)-mLen; i++ {
		match := true
		for j := 0; j < mLen; j++ {
			// If both are empty lines, they match
			if normOldLines[j] == "" && fileLines[i+j].norm == "" {
				continue
			}
			if fileLines[i+j].norm != normOldLines[j] {
				match = false
				break
			}
		}
		if match {
			matchIndices = append(matchIndices, i)
		}
	}

	// Ensure unique, unambiguous match
	if len(matchIndices) == 1 {
		startIdx := matchIndices[0]
		endIdx := startIdx + mLen - 1

		// An audit caught that the line-0 fix below (restoring the file's
		// real leading whitespace so old_string matches byte-for-byte) was
		// never extended to lines 1..N: new_string's matching lines keep
		// whatever indentation the model wrote them with — typically none,
		// mirroring its own flawed old_string — so the replace strips the
		// file's real indentation from every line after the first,
		// corrupting the result (especially syntactically significant for
		// Python/YAML). Record each line's missing indentation here so the
		// caller can re-apply it to new_string.
		// An audit further caught that checking "old_string's line already
		// had SOME leading whitespace" (any space/tab) was too weak: a
		// model that writes old_string with its own space-indentation
		// against a tab-indented file passes that check and gets treated
		// as "nothing missing", even though the style/depth is wrong — the
		// replacement then injects space-indented lines into a tab file
		// (or vice versa), corrupting Go/Python/YAML indentation rules
		// just as badly as missing indentation does. Compare against the
		// file's actual leading whitespace for that line, not just
		// presence vs. absence, so a mismatched style/depth is corrected
		// too, not only a totally absent one.
		lineIndents := make([]string, mLen)
		for j := 0; j < mLen; j++ {
			fileLineClean := fileLines[startIdx+j].clean
			fileLeadingLen := len(fileLineClean) - len(strings.TrimLeft(fileLineClean, " \t"))
			fileIndent := fileLineClean[:fileLeadingLen]
			oldLeadingLen := len(rawOldLines[j]) - len(strings.TrimLeft(rawOldLines[j], " \t"))
			oldIndent := rawOldLines[j][:oldLeadingLen]
			if fileIndent == oldIndent {
				// GAP-ED-01: When old_string's indentation on line j already matched
				// the file, do not record a correction. This preserves the model's
				// intentional indentation changes in new_string (e.g. de-denting or
				// re-indenting lines) rather than forcing every line back to the
				// file's pre-edit indentation.
				continue
			}
			lineIndents[j] = fileIndent
		}
		// Captured unconditionally (unlike lineIndents[0], which stays ""
		// when old_string's line 0 already matched) so the caller has the
		// file's real line-0 indentation available even in that matching
		// case — needed below for a single-line-to-multi-line replacement,
		// where lines 1..N never existed in old_string at all and so have
		// no indent of their own to detect a mismatch against.
		fileLine0Clean := fileLines[startIdx].clean
		fileLine0Indent := fileLine0Clean[:len(fileLine0Clean)-len(strings.TrimLeft(fileLine0Clean, " \t"))]

		startOffset := fileLines[startIdx].offset
		// normalizeLineForMatch strips leading whitespace for comparison,
		// so a model-written old_string with no leading indentation (e.g.
		// "x = 10;") matches a real file line that DOES have some
		// ("    x = 10;") — but the returned span always started at the raw
		// line's own offset (before that indentation), capturing it as part
		// of old_string. Claude Code then replaces that span with the
		// model's new_string, which is typically written in the same
		// unindented style as its old_string, silently stripping the file's
		// real indentation. If old_string's first line had no leading
		// whitespace of its own, start the returned span after the file
		// line's leading whitespace instead, leaving it as untouched
		// surrounding text outside the replaced span.
		//
		// An audit caught that this shift also fired when old_string's
		// line 0 DID have its own (wrong-style/wrong-depth) leading
		// whitespace — e.g. 4 spaces against a tab-indented file. In that
		// case lineIndents[0] holds the file's real tab, and shifting
		// startOffset past it excludes the real tab from the matched span
		// entirely, leaving it as untouched surrounding text; new_string's
		// line 0 (still carrying its own 4 spaces, since reapplyLineIndents
		// skipped line 0 unconditionally) then gets inserted right after
		// that preserved tab, producing "\t    " — tab AND spaces both
		// present. Only take this shortcut when old_string's own line 0
		// had NO leading whitespace at all; otherwise leave the real
		// indentation inside the matched span (so it gets replaced, not
		// preserved-and-duplicated) and let reapplyLineIndents correct
		// new_string's line 0 the same way it corrects every other line.
		oldLine0LeadingLen := len(rawOldLines[0]) - len(strings.TrimLeft(rawOldLines[0], " \t"))
		if lineIndents[0] != "" && oldLine0LeadingLen == 0 {
			startOffset += len(lineIndents[0])
			// An audit caught a remaining edge case even after the shift:
			// it assumes new_string's line 0 mirrors old_string's own lack
			// of indentation (the common case), but if the model happened
			// to write new_string's line 0 WITH its own leading whitespace
			// anyway (while old_string had none), that whitespace would
			// land right after the real indentation this shift already
			// preserves, doubling it (e.g. "\t\t" instead of "\t"). Use a
			// sentinel so reapplyLineIndents strips any such whitespace
			// from new_string's line 0 without prepending anything — the
			// correct indentation already comes from the preserved span.
			lineIndents[0] = stripOnlyIndentMarker
		}
		lastLine := fileLines[endIdx]

		// If the last line in model's old_string did not end with a newline,
		// strip trailing newline from the matched file segment to match model's boundary intention
		endOffset := lastLine.offset + len(lastLine.text)
		if !strings.HasSuffix(oldStr, "\n") && !strings.HasSuffix(oldStr, "\r") {
			endOffset = lastLine.offset + len(lastLine.clean)
		}

		if endOffset <= len(fileContent) && startOffset <= endOffset {
			return fileContent[startOffset:endOffset], lineIndents, fileLine0Indent, true
		}
	}

	return "", nil, "", false
}
