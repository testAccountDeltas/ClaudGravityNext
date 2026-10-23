package toolschema

import (
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
