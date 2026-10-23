// Package toolkind holds IsModifyingToolName, a small pure classifier
// originally embedded in v1's server.go but genuinely shared by THREE
// unrelated call sites: loopguard.go (did this turn make real progress,
// resetting the read-loop counter), server.go's own request handling
// (twice), and translator.go (deciding tool_choice/safety behavior).
// Those three destination packages (internal/loopguard,
// internal/orchestrate, internal/translate) have no other reason to
// depend on each other, so — same single-ownership rule as
// internal/sessionkey and internal/fakeids elsewhere in this migration —
// this function gets its own small leaf package that all three import,
// rather than living in one of them and creating an artificial
// cross-dependency.
package toolkind

import "strings"

// IsModifyingToolName reports whether name is a tool call that genuinely
// changes a file (Write/Edit/NotebookEdit/str_replace_editor/update, or an
// MCP tool whose own name suggests a write/create/delete/update/insert/
// modify/patch operation).
func IsModifyingToolName(name string) bool {
	lower := strings.ToLower(name)
	// An audit caught this switch was case-sensitive against the exact
	// Claude Code tool names, and missing other real editing tool names
	// entirely (str_replace_editor — the Anthropic text-editor tool's own
	// name in some SDK/tool-use contexts, and "update" as used by some
	// clients). A model calling any of these in a different case (or
	// using str_replace_editor/update) never counted as progress here,
	// so LoopGuard's read-streak counter never reset after a real,
	// successful edit — it could inject a stop-reading nudge immediately
	// after the model just finished modifying the file it was reading.
	switch lower {
	case "write", "edit", "notebookedit", "str_replace_editor", "str_replace_based_edit_tool", "text_editor", "update":
		return true
	}
	if !strings.HasPrefix(lower, "mcp__") {
		return false
	}
	for _, kw := range []string{"write", "edit", "create", "delete", "update", "insert", "modify", "patch"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
