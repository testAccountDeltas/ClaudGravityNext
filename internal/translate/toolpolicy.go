// Part of the translate package — see translate.go for the package doc
// comment. Tool-declaration policy helpers shared by toolnames.go (the
// up-front name pre-pass) and tools.go (the declaration loop) so both
// always agree on which tools reach Gemini.
package translate

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"

	"claudgravity/internal/logx"
	"claudgravity/internal/toolschema"
)

func isGemini3(modelName string) bool {
	return strings.Contains(strings.ToLower(modelName), "gemini-3")
}

// isServerSearchTool reports whether t is a web-search tool that Gemini
// serves natively (googleSearch grounding) rather than a function. A
// CLIENT tool named WebSearch/web_search that carries its own
// input_schema (Claude Code's WebSearch: query, allowed_domains,
// blocked_domains) is an ordinary function the model must be able to
// call — the server-side interception in httpapi/websearch_intercept.go
// handles the single-tool sub-request before translation ever runs.
func isServerSearchTool(t gjson.Result, name string) bool {
	norm := strings.ToLower(strings.ReplaceAll(name, "_", ""))
	if !(strings.HasPrefix(norm, "websearch") || strings.HasPrefix(norm, "googlesearch")) {
		return false
	}
	tt := t.Get("type").String()
	// Only Claude Code's own camel-case name: a snake_case "web_search"
	// function was observed crashing the CLI ("No such tool available").
	if name == "WebSearch" && (tt == "" || tt == "custom") {
		if props := t.Get("input_schema.properties"); props.IsObject() && len(props.Map()) > 0 {
			return false
		}
	}
	return true
}

// isUnsupportedServerTool reports an Anthropic server-tool type this
// gateway has no schema or implementation for (advisor_*, tool_search_*,
// ...): declared without input_schema and not a known built-in. Sent to
// Gemini it would be a function with an empty description and a
// placeholder "reason" parameter.
func isUnsupportedServerTool(t gjson.Result, name string) bool {
	tt := t.Get("type").String()
	if tt == "" || tt == "custom" {
		return false
	}
	if s := strings.TrimSpace(t.Get("input_schema").Raw); s != "" && s != "null" && s != "{}" {
		return false
	}
	if isServerSearchTool(t, name) {
		return false
	}
	if _, ok := toolschema.Builtin(tt); ok {
		return false
	}
	if _, ok := toolschema.Builtin(name); ok {
		return false
	}
	return true
}

// validateToolChoice rejects what Anthropic itself would 400: a forced
// tool that was never declared, and OpenAI-style "required".
func validateToolChoice(root gjson.Result, tn toolNameInfo) error {
	tc := root.Get("tool_choice")
	if !tc.Exists() {
		return nil
	}
	typ := tc.Get("type").String()
	if tc.Type == gjson.String {
		typ = tc.String()
	}
	if typ == "required" {
		return fmt.Errorf(`tool_choice: "required" is not valid; use {"type":"any"}`)
	}
	if typ == "tool" {
		name := tc.Get("name").String()
		if name == "" {
			return fmt.Errorf("tool_choice.name: Field required when tool_choice.type is \"tool\"")
		}
		if !tn.declaredToolNames[name] {
			return fmt.Errorf("tool_choice.name: tool %q is not declared in tools", name)
		}
	}
	return nil
}

// repairedNameAliases returns extra reverse-map entries so a function name
// the model returns in the wrong case, or under another name of the same
// built-in family (read -> Read, str_replace_editor ->
// str_replace_based_edit_tool), maps back to the declared tool. Only
// unambiguous matches are added and a real declared name is never shadowed.
func repairedNameAliases(sent map[string]string, editorName, bashName string) map[string]string {
	byLower := map[string][]string{}
	taken := map[string]bool{}
	for orig, san := range sent {
		taken[orig], taken[san] = true, true
		byLower[strings.ToLower(orig)] = append(byLower[strings.ToLower(orig)], orig)
	}
	out := map[string]string{}
	for lc, origs := range byLower {
		uniq := map[string]bool{}
		for _, o := range origs {
			uniq[o] = true
		}
		if len(uniq) != 1 {
			continue
		}
		for _, o := range origs {
			if lc != o && !taken[lc] {
				out[lc] = o
			}
			// Gemini-bound name, lower-cased, e.g. "mcp__x_read".
			if san := sent[o]; san != "" && strings.ToLower(san) != san && !taken[strings.ToLower(san)] {
				out[strings.ToLower(san)] = o
			}
		}
	}
	if editorName != "" {
		for _, a := range []string{"str_replace_editor", "str_replace_based_edit_tool", "text_editor"} {
			if !taken[a] && out[a] == "" {
				out[a] = editorName
			}
		}
	}
	if bashName != "" && !taken["bash"] && out["bash"] == "" {
		out["bash"] = bashName
	}
	return out
}

func logDroppedServerTool(name, toolType string) {
	logx.LogGateway("[Tools] dropping unsupported server tool %q (type %q): no schema and no gateway implementation", name, toolType)
}
