// Package toolargs normalizes a tool call's raw JSON arguments into an
// object, guaranteed. Extracted out of superrelay (v1)'s sse.go
// (normalizeToolCallArgsToObject) into its own leaf package rather than
// left inside internal/stream: internal/stream needs it when a NEWLY
// generated tool call streams out (HandleFunctionCall), but
// internal/translate independently needs the identical fix when
// REPLAYING a prior tool_use from conversation history — a history
// capture or an older client can carry a non-object input the same way a
// freshly generated one can. Same single-ownership rationale as
// internal/toolsig in this same migration phase.
package toolargs

import (
	"encoding/json"
	"strings"
)

// primaryStringParam names the one argument a tool's bare-string args
// should be mapped onto, for tools whose schema has an obvious single
// required string field a model is likely to collapse its args into.
// "write" is deliberately omitted: Write always requires BOTH file_path
// and content, so a bare string can never fully satisfy it either way,
// and guessing it means "file_path" risks treating the file's intended
// CONTENT as its path instead — falling through to {} (missing both
// params) is a clearer, more honest failure than a plausible-looking but
// incorrect guess.
var primaryStringParam = map[string]string{
	"bash":                        "command",
	"powershell":                  "command",
	"pwsh":                        "command",
	"read":                        "file_path",
	"edit":                        "file_path",
	"notebookedit":                "notebook_path",
	"grep":                        "pattern",
	"glob":                        "pattern",
	"str_replace_editor":          "path",
	"str_replace_based_edit_tool": "path",
	"text_editor":                 "path",
	"web_search":                  "query",
	"websearch":                   "query",
	"web_fetch":                   "url",
	"webfetch":                    "url",
}

func lookupPrimaryParam(name string) (string, bool) {
	nameLower := strings.ToLower(name)
	if param, ok := primaryStringParam[nameLower]; ok {
		return param, true
	}
	normalized := strings.ReplaceAll(nameLower, "-", "_")
	if param, ok := primaryStringParam[normalized]; ok {
		return param, true
	}
	collapsed := strings.ReplaceAll(normalized, "_", "")
	if param, ok := primaryStringParam[collapsed]; ok {
		return param, true
	}
	for prefix, param := range primaryStringParam {
		if strings.HasPrefix(nameLower, prefix+"_") || strings.HasPrefix(nameLower, prefix+"-") {
			return param, true
		}
	}
	return "", false
}

func isShellTool(nameLower string) bool {
	return nameLower == "bash" || strings.HasPrefix(nameLower, "bash_") || strings.HasPrefix(nameLower, "bash-") ||
		nameLower == "powershell" || strings.HasPrefix(nameLower, "powershell_") || strings.HasPrefix(nameLower, "powershell-") ||
		nameLower == "pwsh" || strings.HasPrefix(nameLower, "pwsh_") || strings.HasPrefix(nameLower, "pwsh-")
}

// NormalizeToArgsObject guarantees argsRaw, when parsed as JSON, is an
// object — the Anthropic streaming protocol accumulates
// "input_json_delta" partial_json into tool_use.input, which the
// official SDKs' Zod schemas require to be Record<string, unknown>.
// Gemini occasionally emits a functionCall.args that's itself a bare
// string ("ls -la") or doubly-encoded JSON ("{\"cmd\":\"ls\"}") instead
// of an object — streamed or replayed through verbatim, the client's
// accumulated/parsed value is a string, not an object, crashing SDK
// validation on the Claude side.
func NormalizeToArgsObject(name, argsRaw string) string {
	var v interface{}
	if err := strictDecode(argsRaw, &v); err != nil {
		// Gemini commonly emits a literal, unescaped control character
		// (0x0A newline, 0x0D carriage return, 0x09 tab) inside a
		// multi-line string argument — e.g. a Bash heredoc or a Write
		// tool's file content — which Go's encoding/json correctly
		// rejects per the JSON spec ("invalid character '\n' in string
		// literal"). Before falling back to an empty object (which
		// silently drops EVERY argument, including the required
		// "command"/"content" field, and crashes Claude Code with
		// "Missing required parameter"), retry once against a repaired
		// copy with control characters inside string literals escaped.
		repaired := repairUnescapedControlChars(argsRaw)
		if repaired == argsRaw {
			return "{}"
		}
		if err2 := strictDecode(repaired, &v); err2 != nil {
			return "{}"
		}
		argsRaw = repaired
	}
	wasDoubleEncoded := false
	if s, ok := v.(string); ok {
		var inner interface{}
		if err := strictDecode(s, &inner); err == nil {
			v = inner
			wasDoubleEncoded = true
		}
	}
	if s, ok := v.(string); ok {
		// A bare string that ISN'T double-encoded JSON — Gemini just
		// emitting args:"git status" instead of args:{"command":"git
		// status"} — maps onto the tool's one obvious primary string
		// parameter when known, rather than silently dropping the
		// model's actual command.
		if param, ok := lookupPrimaryParam(name); ok {
			b, err := json.Marshal(map[string]interface{}{param: s})
			if err == nil {
				return string(b)
			}
		}
		return "{}"
	}
	m, ok := v.(map[string]interface{})
	if !ok {
		return "{}"
	}

	nameLower := strings.ToLower(name)

	// GAP-JS-09: If a tool call's arguments were wrapped in {"__scalar_arg__": <val>},
	// unwrap __scalar_arg__ into a valid object Record<string, unknown>.
	// Anthropic Messages API strictly requires tool_use.input to ALWAYS be a JSON object.
	if scalarVal, exists := m["__scalar_arg__"]; exists {
		if innerMap, isMap := scalarVal.(map[string]interface{}); isMap {
			m = innerMap
			v = m
			wasDoubleEncoded = true
		} else if s, isStr := scalarVal.(string); isStr {
			var innerObj interface{}
			if err := strictDecode(s, &innerObj); err == nil {
				if innerMap, ok := innerObj.(map[string]interface{}); ok {
					m = innerMap
					v = m
					wasDoubleEncoded = true
				} else if param, ok := lookupPrimaryParam(nameLower); ok {
					b, _ := json.Marshal(map[string]interface{}{param: innerObj})
					return string(b)
				} else {
					b, _ := json.Marshal(map[string]interface{}{"value": innerObj})
					return string(b)
				}
			} else if param, ok := lookupPrimaryParam(nameLower); ok {
				b, err := json.Marshal(map[string]interface{}{param: s})
				if err == nil {
					return string(b)
				}
			} else {
				b, err := json.Marshal(map[string]interface{}{"value": s})
				if err == nil {
					return string(b)
				}
			}
		} else if param, ok := lookupPrimaryParam(nameLower); ok {
			b, err := json.Marshal(map[string]interface{}{param: scalarVal})
			if err == nil {
				return string(b)
			}
		} else {
			b, err := json.Marshal(map[string]interface{}{"value": scalarVal})
			if err == nil {
				return string(b)
			}
		}
	}

	if len(m) == 1 {
		if argsStr, ok := m["args"].(string); ok && strings.HasPrefix(strings.TrimSpace(argsStr), "{") {
			var innerMap map[string]interface{}
			if err := json.Unmarshal([]byte(argsStr), &innerMap); err == nil {
				m = innerMap
				v = m
				wasDoubleEncoded = true
			}
		}
	}

	// Type coercion and null handling are schema-driven (ConformToSchema,
	// applied on the response path via schema.CoerceNumericEnumArgs); this
	// function only guarantees an object.
	if !wasDoubleEncoded {
		return argsRaw // already a plain object, avoid an unnecessary re-marshal
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
