package toolschema

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuiltin_ToolsWithoutDateSuffix(t *testing.T) {
	tests := []struct {
		name     string
		toolType string
		wantKey  string
	}{
		{"bash bare", "bash", `"command"`},
		{"bash dated", "bash_20250124", `"command"`},
		{"text_editor bare", "text_editor", `"command"`},
		{"text_editor dated", "text_editor_20241022", `"command"`},
		{"computer bare", "computer", `"action"`},
		{"computer dated", "computer_20241022", `"action"`},
		{"computer_toolset dated", "computer_toolset_20260801", `"action"`},
		{"web_fetch bare", "web_fetch", `"url"`},
		{"web_fetch dated", "web_fetch_20250101", `"url"`},
		{"code_execution bare", "code_execution", `"code"`},
		{"code_execution dated", "code_execution_20250101", `"code"`},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			schema, ok := Builtin(tc.toolType)
			if !ok {
				t.Fatalf("expected Builtin(%q) to return true", tc.toolType)
			}
			if !strings.Contains(schema, tc.wantKey) {
				t.Fatalf("expected schema for %q to contain %q, got: %s", tc.toolType, tc.wantKey, schema)
			}
		})
	}
}

func TestBuiltin_UnknownTool(t *testing.T) {
	schema, ok := Builtin("custom_tool")
	if ok || schema != "" {
		t.Fatalf("expected unknown tool to return empty schema and false, got (%q, %v)", schema, ok)
	}
}

func TestBuiltin_MemoryTextEditorComputerShapes(t *testing.T) {
	mem, _ := Builtin("memory_20250818")
	for _, want := range []string{`"rename"`, `"old_path"`, `"insert_text"`, `"file_text"`, `"delete"`} {
		if !strings.Contains(mem, want) {
			t.Fatalf("memory schema lacks %s: %s", want, mem)
		}
	}
	if strings.Contains(mem, `"key"`) || strings.Contains(mem, `"action"`) {
		t.Fatalf("memory schema must not use the legacy action/key shape: %s", mem)
	}
	modern, _ := Builtin("text_editor_20250728")
	if strings.Contains(modern, "undo_edit") || !strings.Contains(modern, `"insert_text"`) {
		t.Fatalf("20250728 editor: no undo_edit, insert_text required: %s", modern)
	}
	old, _ := Builtin("text_editor_20250124")
	if !strings.Contains(old, "undo_edit") {
		t.Fatalf("older editor keeps undo_edit: %s", old)
	}
	comp, _ := Builtin("computer_20250124")
	if !strings.Contains(comp, `"start_coordinate"`) {
		t.Fatalf("computer schema lacks start_coordinate: %s", comp)
	}
	for _, s := range []string{mem, modern, old, comp} {
		if !json.Valid([]byte(s)) {
			t.Fatalf("invalid JSON schema: %s", s)
		}
	}
}
