// Package sanitize ports superrelay (v1)'s sanitizer.go — AST-level
// pre-flight cleanup of a Claude conversation before it's translated and
// sent to Gemini — onto the typed internal/protocol/claude domain instead
// of map[string]interface{}.
//
// This buys a real simplification, not just a type-safety label: v1's
// extractPlainText/content-normalization needed two near-duplicate loops
// (one for []map[string]interface{}, one for raw-from-json []interface{})
// because a value could arrive in either shape depending on how many times
// SanitizeMessagesAndGraph had already run over it in the same request —
// the exact "divergent duplication" root cause the migration spec (Part
// 1.2) diagnosed. claude.Message's own UnmarshalJSON normalizes content to
// []claude.Block exactly once, on decode, so every helper in this package
// only ever sees one shape.
package sanitize

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"claudgravity/internal/protocol/claude"
)

// ToolOutputCharLimit is the eager middle-truncation threshold for a
// tool_result's text content (64 KB) — same value as v1's own constant.
const ToolOutputCharLimit = 65536

var (
	pollutedToolCallTextPattern = regexp.MustCompile(`(?i)\[Called tool [^\]]*\]`)
	multiNewlinePattern         = regexp.MustCompile(`\n{3,}`)
	ansiEscapePattern           = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\([a-zA-Z]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)`)
	syntheticXMLTagsPattern     = regexp.MustCompile(`(?i)</?task[-_]notification>|(?i)</?task[-_]result>`)
)

// StripSyntheticXMLTags strips out pseudo-system XML tags such as <task-notification>,
// </task-notification>, <task_result>, </task_result> that models might hallucinate
// from session history, preventing terminal UI corruption in Claude Code CLI.
func StripSyntheticXMLTags(content string) string {
	if !strings.Contains(content, "task-") && !strings.Contains(content, "task_") {
		return content
	}
	return syntheticXMLTagsPattern.ReplaceAllString(content, "")
}

// PartialSyntheticTagSuffix returns the trailing substring if s ends with a partial,
// unclosed prefix of a synthetic task tag (e.g. "<task-", "</task-notif"), or empty string.
func PartialSyntheticTagSuffix(s string) string {
	idx := strings.LastIndex(s, "<")
	if idx < 0 {
		return ""
	}
	candidate := strings.ToLower(s[idx:])
	if strings.Contains(candidate, ">") {
		return ""
	}
	prefixes := [...]string{
		"<task-notification>",
		"</task-notification>",
		"<task_notification>",
		"</task_notification>",
		"<task-result>",
		"</task-result>",
		"<task_result>",
		"</task_result>",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(p, candidate) {
			return s[idx:]
		}
	}
	return ""
}

// StripANSIEscapeCodes strips ANSI escape sequences (colors, styles, cursor control)
// from text so terminal outputs do not confuse models or corrupt Edit tool matching.
func StripANSIEscapeCodes(content string) string {
	if !strings.Contains(content, "\x1b") {
		return content
	}
	return ansiEscapePattern.ReplaceAllString(content, "")
}

// extractPlainText concatenates the text of every "text" block in blocks,
// joining with "\n" when a prior block's text didn't already end in one —
// same join rule as v1's extractPlainText, minus the dual-shape switch
// (see package doc comment).
func extractPlainText(blocks []claude.Block) string {
	var sb strings.Builder
	for _, b := range blocks {
		t, ok := b.AsText()
		if !ok {
			continue
		}
		if sb.Len() > 0 && !strings.HasSuffix(sb.String(), "\n") {
			sb.WriteString("\n")
		}
		sb.WriteString(t.Text)
	}
	return sb.String()
}

// toolResultPlainText extracts the text out of a tool_result's dynamic
// Content (a bare string, or an array mixing text blocks and/or bare
// strings — only text contributes). Unlike extractPlainText, items are
// concatenated with no separator — matches v1's own toolResultPlainText.
func toolResultPlainText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(content, &s); err == nil {
		return s
	}
	var items []json.RawMessage
	if err := json.Unmarshal(content, &items); err != nil {
		return ""
	}
	var sb strings.Builder
	for _, item := range items {
		var str string
		if err := json.Unmarshal(item, &str); err == nil {
			sb.WriteString(str)
			continue
		}
		var blk struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(item, &blk); err == nil && blk.Type == "text" {
			sb.WriteString(blk.Text)
		}
	}
	return sb.String()
}

// StripPollutedToolCallText strips out pseudo-tool call narrations such as
// "[Called tool exec_command with input {...}]" which confuse models into
// generating tool invocations as raw text instead of structured JSON
// calls. Direct port of v1's function of the same name.
func StripPollutedToolCallText(content string) string {
	if !strings.Contains(strings.ToLower(content), "[called tool") {
		return content
	}
	cleaned := pollutedToolCallTextPattern.ReplaceAllString(content, "")
	cleaned = multiNewlinePattern.ReplaceAllString(cleaned, "\n\n")
	return strings.TrimSpace(cleaned)
}

// TruncateMiddle truncates large text from the center, leaving head and
// tail intact, on valid UTF-8 rune boundaries. Direct port of v1's
// function of the same name, including the fixed truncateMarkerBudget
// that keeps a re-truncated result from ever re-triggering truncation on a
// later pass (see superrelay/sanitizer.go's own comment on this).
//
// GAP-T7: if text is itself a valid JSON array (a common tool_result shape
// — grep/search matches, a paginated listing, log lines), a raw byte slice
// through the middle lands inside some element's string/number literal
// and produces invalid JSON the model then can't parse at all. In that
// case, truncate at the element boundary instead (drop whole middle
// elements, keep head/tail ones, splice in one string marker element) so
// the result is still valid JSON; any other shape (object, scalar, plain
// text) falls back to the original byte-middle truncation.
func TruncateMiddle(text string) (string, bool) {
	n := len(text)
	if n <= ToolOutputCharLimit {
		return text, false
	}
	if truncated, ok := truncateJSONArrayMiddle(text); ok {
		return truncated, true
	}
	const truncateMarkerBudget = 128
	half := (ToolOutputCharLimit - truncateMarkerBudget) / 2

	headIdx := half
	for headIdx > 0 && !utf8.RuneStart(text[headIdx]) {
		headIdx--
	}

	tailIdx := n - half
	for tailIdx < n && !utf8.RuneStart(text[tailIdx]) {
		tailIdx++
	}

	marker := fmt.Sprintf("\n<truncated %d of %d chars from the middle of the output>\n", tailIdx-headIdx, n)
	return text[:headIdx] + marker + text[tailIdx:], true
}

// truncateJSONArrayMiddle implements the JSON-array case documented on
// TruncateMiddle above. ok==false (fall back to byte truncation) when text
// isn't a JSON array, has too few elements to usefully trim, or the
// trimmed result still doesn't fit comfortably under the limit (an array
// of a few huge elements gains nothing from dropping whole elements).
func truncateJSONArrayMiddle(text string) (string, bool) {
	var elems []json.RawMessage
	if err := json.Unmarshal([]byte(text), &elems); err != nil || len(elems) < 3 {
		return "", false
	}
	// Budget by element count: keep roughly half the surviving elements at
	// head and half at tail, shrinking until the re-marshaled result fits,
	// or giving up once too few elements remain to be worth it.
	keep := len(elems) - 1
	for keep >= 2 {
		headN := (keep + 1) / 2
		tailN := keep - headN
		dropped := len(elems) - headN - tailN
		marker, _ := json.Marshal(fmt.Sprintf("<%d of %d array elements truncated here>", dropped, len(elems)))
		out := make([]json.RawMessage, 0, headN+1+tailN)
		out = append(out, elems[:headN]...)
		out = append(out, marker)
		out = append(out, elems[len(elems)-tailN:]...)
		b, err := json.Marshal(out)
		if err == nil && len(b) <= ToolOutputCharLimit {
			return string(b), true
		}
		keep--
	}
	return "", false
}
