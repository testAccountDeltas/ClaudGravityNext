// Package toolschema owns the reference input_schema for Anthropic's
// server-side built-in tools (bash, text_editor, computer use — web_search
// is handled separately via native Gemini googleSearch grounding and never
// reaches this path). Anthropic's own clients declare these with only
// {"type": "<dated-version>", "name": "..."} and no input_schema at all —
// Anthropic's server fills in the real parameters itself — but this
// gateway has no equivalent server-side knowledge, so without this, an
// empty input_schema told Gemini the tool takes nothing but a synthetic
// "reason" string.
//
// Extracted out of superrelay (v1)'s translator.go rather than duplicated:
// internal/tokens needs it to price a built-in tool's real parameter
// schema, and the future translate port (which actually injects this
// schema into the Gemini-bound request) needs the identical copy.
package toolschema

import "strings"

// Builtin returns the reference input_schema for toolType, matched by
// prefix since Anthropic dates each version (bash_20250124, bash_20241022,
// ...) — the parameter shapes have stayed stable across dates for each
// tool family, so one schema per family is sufficient.
func Builtin(toolType string) (string, bool) {
	norm := strings.ToLower(toolType)
	switch {
	case norm == "bash" || strings.HasPrefix(norm, "bash_"):
		return `{"type":"object","properties":{"command":{"type":"string","description":"The bash command to run."},"restart":{"type":"boolean","description":"Set to true to restart the tool instead of running a command."}},"required":["command"]}`, true
	case norm == "text_editor" || strings.HasPrefix(norm, "text_editor_"):
		return `{"type":"object","properties":{"command":{"type":"string","enum":["view","create","str_replace","insert","undo_edit"],"description":"The editor command to run."},"path":{"type":"string","description":"Absolute path to the file or directory."},"file_text":{"type":"string","description":"Required for create: the full content of the new file."},"old_str":{"type":"string","description":"Required for str_replace: the exact text to replace."},"new_str":{"type":"string","description":"Replacement text for str_replace, or the text to insert for insert."},"insert_line":{"type":"integer","description":"Required for insert: the line number after which to insert."},"view_range":{"type":"array","items":{"type":"integer"},"description":"Optional for view: [start_line, end_line]."}},"required":["command","path"]}`, true
	case norm == "computer" || strings.HasPrefix(norm, "computer_"):
		return `{"type":"object","properties":{"action":{"type":"string","enum":["key","hold_key","type","cursor_position","mouse_move","left_mouse_down","left_mouse_up","left_click","left_click_drag","right_click","middle_click","double_click","triple_click","scroll","wait","screenshot"],"description":"The computer-use action to perform."},"coordinate":{"type":"array","items":{"type":"integer"},"description":"[x, y] pixel coordinate for click/move/drag actions."},"text":{"type":"string","description":"Text for key/type actions."},"duration":{"type":"integer","description":"Seconds to hold a key or wait."},"scroll_direction":{"type":"string","enum":["up","down","left","right"]},"scroll_amount":{"type":"integer"}},"required":["action"]}`, true
	case norm == "web_fetch" || strings.HasPrefix(norm, "web_fetch_"):
		// GAP-TL-03
		return `{"type":"object","properties":{"url":{"type":"string","description":"The URL to fetch and render."},"format":{"type":"string","enum":["text","markdown","html"],"description":"The format to return the page content in."}},"required":["url"]}`, true
	case norm == "code_execution" || strings.HasPrefix(norm, "code_execution_"):
		// GAP-TL-03
		return `{"type":"object","properties":{"code":{"type":"string","description":"The code to execute."},"language":{"type":"string","description":"The programming language of code (e.g. python, bash)."}},"required":["code"]}`, true
	case norm == "memory" || strings.HasPrefix(norm, "memory_"):
		return `{"type":"object","properties":{"action":{"type":"string","enum":["view","create","update","delete"],"description":"The action to perform on the memory entry."},"key":{"type":"string","description":"Key or slug of the memory item."},"value":{"type":"string","description":"Content or value of the memory item."}},"required":["action"]}`, true
	}
	return "", false
}
