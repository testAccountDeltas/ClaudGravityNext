// Part of the editrepair package's port from superrelay (v1)'s
// edit_repair.go — see cwd.go for the split rationale.
package editrepair

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// ExtractSystemPromptText pulls the plain-text system prompt out of an
// Anthropic request body, decoding whichever shape Claude Code sent it in
// (a bare string, or an array of {"type":"text","text":...} blocks with
// cache_control metadata). Callers must use this — or gjson's .String(), not
// .Raw — before handing text to any regex/string search: .Raw returns the
// literal source JSON bytes with escape sequences un-decoded (a real
// newline inside the prompt is still the two ASCII characters '\' 'n' in
// .Raw, not a newline byte), which silently breaks any "stop at \n" pattern.
func ExtractSystemPromptText(bodyBytes []byte) string {
	sys := gjson.GetBytes(bodyBytes, "system")
	if sys.Type == gjson.String {
		return sys.String()
	}
	if sys.IsArray() {
		var sb strings.Builder
		sys.ForEach(func(_, block gjson.Result) bool {
			sb.WriteString(block.Get("text").String())
			sb.WriteString("\n")
			return true
		})
		return sb.String()
	}
	return ""
}

var cwdFromPromptPattern = regexp.MustCompile(`(?:Primary working directory|Current working directory|working directory)[:\s]+([^\r\n]+)`)

// ExtractCWDFromSystemPrompt extracts the user session working directory from Claude Code environment preamble
func ExtractCWDFromSystemPrompt(systemPrompt string) string {
	if systemPrompt == "" {
		return ""
	}
	m := cwdFromPromptPattern.FindStringSubmatch(systemPrompt)
	if len(m) > 1 {
		p := strings.TrimSpace(m[1])
		p = strings.Trim(p, "\"'`\\")
		return filepath.Clean(p)
	}
	return ""
}
