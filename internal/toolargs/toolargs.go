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
	"strconv"
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
	"bash":               "command",
	"powershell":         "command",
	"pwsh":               "command",
	"read":               "file_path",
	"edit":               "file_path",
	"notebookedit":       "notebook_path",
	"grep":               "pattern",
	"glob":               "pattern",
	"str_replace_editor": "path",
	"web_search":         "query",
	"websearch":          "query",
	"web_fetch":          "url",
	"webfetch":           "url",
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
	if err := json.Unmarshal([]byte(argsRaw), &v); err != nil {
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
		if err2 := json.Unmarshal([]byte(repaired), &v); err2 != nil {
			return "{}"
		}
		argsRaw = repaired
	}
	wasDoubleEncoded := false
	if s, ok := v.(string); ok {
		var inner interface{}
		if err := json.Unmarshal([]byte(s), &inner); err == nil {
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
			if err := json.Unmarshal([]byte(s), &innerObj); err == nil {
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

	prunedNulls := pruneNulls(m)
	coercedBooleans := coerceBooleanStrings(m)
	coercedNumbers := coerceNumericStrings(m)
	if !wasDoubleEncoded && !coercedNumbers && !coercedBooleans && !prunedNulls {
		return argsRaw // already a plain object, avoid an unnecessary re-marshal
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func pruneNulls(v interface{}) bool {
	changed := false
	switch val := v.(type) {
	case map[string]interface{}:
		for k, item := range val {
			if item == nil {
				delete(val, k)
				changed = true
			} else if pruneNulls(item) {
				changed = true
			}
		}
	case []interface{}:
		for _, item := range val {
			if pruneNulls(item) {
				changed = true
			}
		}
	}
	return changed
}

func isExemptStringField(key string) bool {
	k := strings.ToLower(key)
	if strings.HasSuffix(k, "id") || strings.HasSuffix(k, "uuid") ||
		strings.HasSuffix(k, "key") || strings.HasSuffix(k, "version") ||
		strings.HasSuffix(k, "sha") || strings.HasSuffix(k, "token") ||
		strings.HasSuffix(k, "hash") || strings.HasSuffix(k, "status") ||
		strings.HasSuffix(k, "model") || strings.HasSuffix(k, "tag") ||
		strings.HasSuffix(k, "branch") || strings.HasSuffix(k, "ref") ||
		strings.HasSuffix(k, "mode") || strings.HasSuffix(k, "type") ||
		strings.HasSuffix(k, "name") {
		return true
	}
	switch k {
	case "file_path", "path", "pattern", "command", "code", "query", "text", "content",
		"old_string", "new_string", "old_str", "new_str", "url", "uri", "id", "name",
		"key", "prompt", "message", "filename", "filepath", "source", "dest", "destination",
		"taskid", "task_id", "agentid", "agent_id", "sessionid", "session_id", "shell_id",
		"status", "hash", "model", "branch", "tag", "ref", "commit", "title", "description",
		"summary", "body", "comment", "value", "mode", "slug", "author", "header", "headers",
		"env", "environment", "zip", "phone", "postal_code", "label", "question", "preview",
		"subject", "activeform", "active_form", "skill", "script", "scriptpath", "script_path",
		"target", "port", "color", "hex", "filter", "output", "input", "context", "note", "notes",
		"reason", "explanation", "revision", "rev", "category", "short_summary", "failure_scenario",
		"details", "area", "ts", "thread_ts", "channel", "repo", "base", "head", "sql", "phone_number":
		return true
	}
	return false
}

func isStringDictContainer(key string) bool {
	k := strings.ToLower(key)
	return k == "env" || k == "environment" || k == "headers" || k == "metadata" || k == "params" || k == "annotations" || k == "answers"
}

func coerceNumericStrings(v interface{}) bool {
	return coerceNumericStringsWithContext(v, "")
}

func coerceNumericStringsWithContext(v interface{}, parentKey string) bool {
	changed := false
	switch val := v.(type) {
	case map[string]interface{}:
		if isStringDictContainer(parentKey) {
			return false
		}
		for k, item := range val {
			if s, isStr := item.(string); isStr {
				if !isExemptStringField(k) {
					sTrim := strings.TrimSpace(s)
					if len(sTrim) > 0 && !(len(sTrim) > 1 && sTrim[0] == '0' && sTrim[1] >= '0' && sTrim[1] <= '9') {
						if n, err := strconv.ParseInt(sTrim, 10, 64); err == nil && strconv.FormatInt(n, 10) == sTrim {
							val[k] = n
							changed = true
						} else if f, err := strconv.ParseFloat(sTrim, 64); err == nil && !strings.ContainsAny(sTrim, "eE") && strings.Contains(sTrim, ".") {
							val[k] = f
							changed = true
						}
					}
				}
			} else if coerceNumericStringsWithContext(item, k) {
				changed = true
			}
		}
	case []interface{}:
		for _, item := range val {
			if coerceNumericStringsWithContext(item, parentKey) {
				changed = true
			}
		}
	}
	return changed
}

func isBooleanField(key string) bool {
	k := strings.ToLower(key)
	switch k {
	case "replace_all", "replaceall", "dangerouslydisablesandbox", "dangerously_disable_sandbox",
		"run_in_background", "runinbackground", "multiselect", "multi_select", "allow_large",
		"allowlarge", "stop", "noop", "force", "pin", "unpin", "is_error", "iserror":
		return true
	}
	return false
}

func coerceBooleanStrings(v interface{}) bool {
	changed := false
	switch val := v.(type) {
	case map[string]interface{}:
		for k, item := range val {
			if s, isStr := item.(string); isStr {
				if isBooleanField(k) {
					sLower := strings.TrimSpace(strings.ToLower(s))
					if sLower == "true" || sLower == "1" {
						val[k] = true
						changed = true
					} else if sLower == "false" || sLower == "0" {
						val[k] = false
						changed = true
					}
				}
			} else if coerceBooleanStrings(item) {
				changed = true
			}
		}
	case []interface{}:
		for _, item := range val {
			if coerceBooleanStrings(item) {
				changed = true
			}
		}
	}
	return changed
}
