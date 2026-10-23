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
		return textEditorSchema(norm), true
	case norm == "computer" || strings.HasPrefix(norm, "computer_"):
		return `{"type":"object","properties":{"action":{"type":"string","enum":["key","hold_key","type","cursor_position","mouse_move","left_mouse_down","left_mouse_up","left_click","left_click_drag","right_click","middle_click","double_click","triple_click","scroll","wait","screenshot","zoom"],"description":"The computer-use action to perform."},"coordinate":{"type":"array","items":{"type":"integer"},"description":"[x, y] pixel coordinate for click/move actions, and the END point of left_click_drag."},"start_coordinate":{"type":"array","items":{"type":"integer"},"description":"[x, y] pixel coordinate where left_click_drag starts (required for left_click_drag)."},"region":{"type":"array","items":{"type":"integer"},"description":"[x0, y0, x1, y1] corners of the area to capture for the zoom action."},"text":{"type":"string","description":"Text for type; key or key combo (e.g. ctrl+s) for key/hold_key; optional modifier keys (shift, ctrl, alt, super) held during click/scroll."},"duration":{"type":"integer","description":"Seconds to hold a key or wait."},"scroll_direction":{"type":"string","enum":["up","down","left","right"]},"scroll_amount":{"type":"integer"}},"required":["action"]}`, true
	case norm == "web_fetch" || strings.HasPrefix(norm, "web_fetch_"):
		// GAP-TL-03
		return `{"type":"object","properties":{"url":{"type":"string","description":"The URL to fetch and render."},"format":{"type":"string","enum":["text","markdown","html"],"description":"The format to return the page content in."}},"required":["url"]}`, true
	case norm == "code_execution" || strings.HasPrefix(norm, "code_execution_"):
		// GAP-TL-03
		return `{"type":"object","properties":{"code":{"type":"string","description":"The code to execute."},"language":{"type":"string","description":"The programming language of code (e.g. python, bash)."}},"required":["code"]}`, true
	case norm == "memory" || strings.HasPrefix(norm, "memory_"):
		return memorySchema, true
	}
	return "", false
}

// memorySchema follows Anthropic's memory tool (memory_20250818): one
// "command" selecting view/create/str_replace/insert/delete/rename, with
// paths under /memories.
const memorySchema = `{"type":"object","properties":{"command":{"type":"string","enum":["view","create","str_replace","insert","delete","rename"],"description":"The memory operation to perform."},"path":{"type":"string","description":"Path under /memories (view, create, str_replace, insert, delete)."},"view_range":{"type":"array","items":{"type":"integer"},"description":"Optional for view on a file: [start_line, end_line]; end_line -1 means end of file."},"file_text":{"type":"string","description":"Required for create: the content of the file."},"old_str":{"type":"string","description":"Required for str_replace: the text to replace (must be unique)."},"new_str":{"type":"string","description":"Optional for str_replace: the replacement text; omitted deletes old_str."},"insert_line":{"type":"integer","description":"Required for insert: insert after this line; 0 inserts at the beginning."},"insert_text":{"type":"string","description":"Required for insert: the text to insert."},"old_path":{"type":"string","description":"Required for rename: the current path."},"new_path":{"type":"string","description":"Required for rename: the new path."}},"required":["command"]}`

// textEditorSchema follows Anthropic's text editor tool
// (str_replace_based_edit_tool). From text_editor_20250728 on, insert
// takes insert_text and there is no undo_edit; the older versions
// (20241022, 20250124, 20250429) insert via new_str and still have undo_edit.
func textEditorSchema(norm string) string {
	modern := norm == "text_editor" || norm >= "text_editor_20250728"
	cmds := `["view","create","str_replace","insert"]`
	insertField := `"insert_text":{"type":"string","description":"Required for insert: the text to insert after insert_line."}`
	newStrDesc := "Replacement text for str_replace."
	if !modern {
		cmds = `["view","create","str_replace","insert","undo_edit"]`
		insertField = `"insert_text":{"type":"string","description":"Text for insert (older clients may send it as new_str)."}`
		newStrDesc = "Replacement text for str_replace, or the text to insert for insert."
	}
	return `{"type":"object","properties":{"command":{"type":"string","enum":` + cmds + `,"description":"The editor command to run."},"path":{"type":"string","description":"Absolute path to the file or directory."},"file_text":{"type":"string","description":"Required for create: the full content of the new file."},"old_str":{"type":"string","description":"Required for str_replace: the exact text to replace (must match exactly one location)."},"new_str":{"type":"string","description":"` + newStrDesc + `"},"insert_line":{"type":"integer","description":"Required for insert: the line number after which to insert (0 = beginning)."},` + insertField + `,"view_range":{"type":"array","items":{"type":"integer"},"description":"Optional for view: [start_line, end_line]; end_line -1 means end of file."}},"required":["command","path"]}`
}
