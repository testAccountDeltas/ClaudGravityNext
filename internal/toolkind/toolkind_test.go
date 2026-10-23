package toolkind

import "testing"

func TestIsModifyingToolName_KnownEditingTools(t *testing.T) {
	for _, name := range []string{"Write", "edit", "NotebookEdit", "str_replace_editor", "Update"} {
		if !IsModifyingToolName(name) {
			t.Errorf("expected %q to be classified as modifying", name)
		}
	}
}

func TestIsModifyingToolName_ReadIsNotModifying(t *testing.T) {
	if IsModifyingToolName("Read") || IsModifyingToolName("Bash") {
		t.Fatal("Read/Bash must not be classified as modifying by name alone")
	}
}

func TestIsModifyingToolName_MCPToolByKeyword(t *testing.T) {
	if !IsModifyingToolName("mcp__filesystem__write_file") {
		t.Fatal("an mcp__ tool whose name contains 'write' must be classified as modifying")
	}
	if IsModifyingToolName("mcp__filesystem__list_dir") {
		t.Fatal("an mcp__ tool with no write/edit/create/delete/update/insert/modify/patch keyword must not be classified as modifying")
	}
}
