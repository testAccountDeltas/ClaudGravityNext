// Part of the translate package — see translate.go for the package doc
// comment. Direct port of v1's translator.go tool-name sanitization and
// collision-disambiguation pre-pass (the start of Step 5, before any
// message history is translated) — computed once, up front, so that
// history replay (messages.go) and the tools declaration (tools.go)
// can never disagree on a tool's final Gemini-bound name.
package translate

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// DropSearchTools is stripped from the declared tools when
// tenant.StripSearchTools is true (default: true — see superrelay's
// README.md's "Почему strip_search_tools по умолчанию true" for the full
// reasoning). Deliberate, not a forgotten default: Grep/Glob's schema
// gives Gemini real room to drop the required field or confuse which
// field a flag belongs in, and unlike Edit there is no defensive repair
// pass for these two to catch and fix a malformed call before it reaches
// the client. Without them, Gemini falls back to Bash for search — a
// predictable, visible failure mode instead of a silently malformed
// structured call.
var DropSearchTools = map[string]bool{
	"Grep": true,
	"Glob": true,
}

// sanitizeGeminiFunctionName rewrites a tool name to satisfy Gemini's
// function-calling protobuf identifier regex (^[a-zA-Z_][a-zA-Z0-9_]*$,
// max 63 chars) — Anthropic's own looser `^[a-zA-Z0-9_-]{1,64}$` permits
// hyphens (common in MCP tool names like "mcp__brave-search__search")
// that Gemini rejects outright. Any character outside [a-zA-Z0-9_]
// becomes "_"; a name that would otherwise start with a digit gets a
// leading "_".
func sanitizeGeminiFunctionName(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		return "_"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	if len(out) > 63 {
		out = out[:63]
	}
	return out
}

// toolNameInfo is the single source of truth, computed once up front,
// for every tool-name decision messages.go's history replay and
// tools.go's declaration loop both need to agree on.
type toolNameInfo struct {
	// declaredToolNames is everything the CLIENT originally declared,
	// before any strip_search_tools/web_search filtering.
	declaredToolNames map[string]bool
	// effectiveToolNames mirrors the SAME filtering tools.go's
	// declaration loop applies — used so messages.go's tool_use/
	// tool_result replay can tell a tool that's merely DECLARED by the
	// client apart from one actually SENT to Gemini this request.
	effectiveToolNames map[string]bool
	// disambiguatedToolName is the final, Gemini-bound sanitized name
	// for each original tool name, including any collision suffix.
	disambiguatedToolName map[string]string
	earlyForcedToolName   string
	toolChoiceType        string
	// keepSearchToolsForForcedAny suppresses strip_search_tools entirely
	// for this one request when tool_choice:"any" is set AND literally
	// every declared tool is a search tool — otherwise a client forcing
	// tool use with only Grep/Glob declared would be left with ZERO
	// tools for Gemini to honor that forced request with.
	keepSearchToolsForForcedAny bool
}

// resolveToolName derives a canonical tool name and the original type string
// from either "name" or "type" (for Anthropic server-side built-ins like
// bash_20250124 or text_editor_20250124).
func resolveToolName(t gjson.Result) (string, string) {
	name := t.Get("name").String()
	toolType := t.Get("type").String()
	if name == "" && toolType != "" {
		if strings.HasPrefix(toolType, "text_editor_") || toolType == "text_editor" {
			name = "str_replace_editor"
		} else if strings.HasPrefix(toolType, "bash_") || toolType == "bash" {
			name = "bash"
		} else {
			name = strings.TrimRight(strings.TrimRightFunc(toolType, func(r rune) bool {
				return r >= '0' && r <= '9'
			}), "_")
			if name == "" {
				name = toolType
			}
		}
	}
	return name, toolType
}

func computeToolNameInfo(root gjson.Result, stripSearchTools bool) toolNameInfo {
	tn := toolNameInfo{
		declaredToolNames:     make(map[string]bool),
		effectiveToolNames:    make(map[string]bool),
		disambiguatedToolName: make(map[string]string),
	}
	claimedSanitizedNames := make(map[string]string) // sanitized name -> the first original name that claimed it

	if tc := root.Get("tool_choice"); tc.Exists() {
		tn.toolChoiceType = tc.Get("type").String()
		if tc.Type == gjson.String {
			tn.toolChoiceType = tc.String()
		}
		if tn.toolChoiceType == "tool" {
			tn.earlyForcedToolName = tc.Get("name").String()
		}
	}
	if tn.toolChoiceType == "any" || tn.toolChoiceType == "required" {
		tn.keepSearchToolsForForcedAny = true
		if toolsArr := root.Get("tools"); toolsArr.IsArray() {
			toolsArr.ForEach(func(_, t gjson.Result) bool {
				n, _ := resolveToolName(t)
				if n == "" {
					return true
				}
				if isServerSearchTool(t, n) || isUnsupportedServerTool(t, n) {
					return true
				}
				if !DropSearchTools[n] {
					tn.keepSearchToolsForForcedAny = false
					return false
				}
				return true
			})
		} else {
			tn.keepSearchToolsForForcedAny = false
		}
	}

	if toolsArr := root.Get("tools"); toolsArr.IsArray() {
		toolsArr.ForEach(func(_, t gjson.Result) bool {
			n, toolType := resolveToolName(t)
			if n == "" {
				return true
			}
			tn.declaredToolNames[n] = true
			if toolType != "" {
				tn.declaredToolNames[toolType] = true
			}
			sanitized := sanitizeGeminiFunctionName(n)
			if existingOrig, claimed := claimedSanitizedNames[sanitized]; claimed && existingOrig != n {
				// Keep incrementing the suffix until an actually-free slot
				// is found — a single fixed "_1" candidate can itself
				// already be claimed by a THIRD colliding name, depending
				// on iteration order.
				base := sanitized
				for n2 := 1; ; n2++ {
					suffix := fmt.Sprintf("_%d", n2)
					candidateBase := base
					if len(candidateBase)+len(suffix) > 63 {
						candidateBase = candidateBase[:63-len(suffix)]
					}
					candidate := candidateBase + suffix
					if existingOrig2, claimed2 := claimedSanitizedNames[candidate]; !claimed2 || existingOrig2 == n {
						sanitized = candidate
						break
					}
				}
			}
			claimedSanitizedNames[sanitized] = n
			tn.disambiguatedToolName[n] = sanitized
			if toolType != "" {
				tn.disambiguatedToolName[toolType] = sanitized
			}
			if isServerSearchTool(t, n) || isUnsupportedServerTool(t, n) {
				return true
			}
			if stripSearchTools && DropSearchTools[n] && n != tn.earlyForcedToolName && !tn.keepSearchToolsForForcedAny {
				return true
			}
			tn.effectiveToolNames[n] = true
			if toolType != "" {
				tn.effectiveToolNames[toolType] = true
			}
			if n == "str_replace_editor" || n == "str_replace_based_edit_tool" || strings.HasPrefix(toolType, "text_editor_") || toolType == "text_editor" {
				tn.declaredToolNames["text_editor"] = true
				tn.disambiguatedToolName["text_editor"] = sanitized
				tn.effectiveToolNames["text_editor"] = true
			}
			return true
		})
	}
	return tn
}
