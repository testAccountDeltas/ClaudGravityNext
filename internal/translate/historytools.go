// Part of the translate package — see translate.go for the package doc comment.
//
// Tools the client executed but no longer lists. Claude Code offers some
// tools (Task*, ...) only as deferred ones: the model may call them, the
// client runs them, yet they are absent from the request's "tools" array.
// Gemini refuses a conversation whose history calls a function that is not
// declared, so those calls used to be replaced by text notes — and the model,
// seeing "(Note: an earlier call to the TaskUpdate tool ... is omitted ...)"
// as its own past output, started writing such notes instead of real calls,
// so the client never ran them (tasks stayed open). Instead the gateway
// re-declares such a tool, reconstructing a loose schema from the arguments
// the conversation actually used, so history stays real functionCalls and
// the model keeps calling the tool the way it did before.
package translate

import (
	"sort"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/logx"
)

// maxHistoryDeclaredTools bounds how many tools are reconstructed per request.
const maxHistoryDeclaredTools = 12

type historyTool struct {
	props map[string]string // property -> JSON schema type
	ok    bool              // at least one call was answered without an error
}

// declareToolsUsedInHistory returns body with a reconstructed declaration
// appended to "tools" for every tool that the conversation called
// successfully but the request does not declare. Tools the gateway removes on
// purpose (the search tools, server tools) are left alone.
func declareToolsUsedInHistory(body []byte) []byte {
	root := gjson.ParseBytes(body)
	msgs := root.Get("messages")
	if !msgs.IsArray() {
		return body
	}
	declared := map[string]bool{}
	root.Get("tools").ForEach(func(_, t gjson.Result) bool {
		n, ty := resolveToolName(t)
		declared[n] = true
		if ty != "" {
			declared[ty] = true
		}
		return true
	})

	used := map[string]*historyTool{}
	nameByID := map[string]string{}
	msgs.ForEach(func(_, m gjson.Result) bool {
		if m.Get("role").String() != "assistant" || !m.Get("content").IsArray() {
			return true
		}
		m.Get("content").ForEach(func(_, b gjson.Result) bool {
			name := b.Get("name").String()
			if b.Get("type").String() != "tool_use" || name == "" || declared[name] || DropSearchTools[name] {
				return true
			}
			ht := used[name]
			if ht == nil {
				ht = &historyTool{props: map[string]string{}}
				used[name] = ht
			}
			nameByID[b.Get("id").String()] = name
			b.Get("input").ForEach(func(k, v gjson.Result) bool {
				if ty := schemaTypeOf(v); ty != "" {
					old, seen := ht.props[k.String()]
					switch {
					case !seen || old == ty:
						ht.props[k.String()] = ty
					case (old == "integer" && ty == "number") || (old == "number" && ty == "integer"):
						ht.props[k.String()] = "number"
					default:
						ht.props[k.String()] = "string" // mixed types: loosest scalar
					}
				}
				return true
			})
			return true
		})
		return true
	})
	if len(used) == 0 {
		return body
	}
	msgs.ForEach(func(_, m gjson.Result) bool {
		if m.Get("role").String() != "user" || !m.Get("content").IsArray() {
			return true
		}
		m.Get("content").ForEach(func(_, b gjson.Result) bool {
			if b.Get("type").String() == "tool_result" && !b.Get("is_error").Bool() {
				if name, ok := nameByID[b.Get("tool_use_id").String()]; ok {
					used[name].ok = true
				}
			}
			return true
		})
		return true
	})

	names := make([]string, 0, len(used))
	for n, ht := range used {
		if ht.ok {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return body
	}
	sort.Strings(names) // stable order: the tools block leads the cached prefix
	if len(names) > maxHistoryDeclaredTools {
		names = names[:maxHistoryDeclaredTools]
	}

	// The task tools come as a family: a TaskCreate that worked means the
	// client also runs TaskUpdate/TaskGet/TaskList, which the model needs in
	// order to close the tasks it opened but which may never have appeared in
	// the history yet.
	extra := map[string]string{}
	for _, n := range names {
		if n == "TaskCreate" {
			for sib, schema := range taskFamilySchemas {
				if !declared[sib] && used[sib] == nil {
					extra[sib] = schema
				}
			}
		}
	}

	out := string(body)
	if !root.Get("tools").IsArray() {
		out, _ = sjson.SetRaw(out, "tools", "[]")
	}
	for _, n := range names {
		keys := make([]string, 0, len(used[n].props))
		for k := range used[n].props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		schema := `{"type":"object","properties":{}}`
		for _, k := range keys {
			schema, _ = sjson.SetRaw(schema, "properties."+escapeSJSONKey(k), `{"type":"`+used[n].props[k]+`"}`)
		}
		if len(keys) == 0 {
			schema = `{}`
		}
		out = appendReconstructedTool(out, n, schema)
	}
	siblings := make([]string, 0, len(extra))
	for n := range extra {
		siblings = append(siblings, n)
	}
	sort.Strings(siblings)
	for _, n := range siblings {
		out = appendReconstructedTool(out, n, extra[n])
	}
	logx.LogGateway("[Tools] re-declared %d tool(s) used earlier in the conversation but missing from this request: %v (task siblings: %v)", len(names), names, siblings)
	return []byte(out)
}

func appendReconstructedTool(out, name, schema string) string {
	tool := `{"name":"","description":"Tool used earlier in this conversation (declaration reconstructed by the gateway).","input_schema":` + schema + `}`
	tool, _ = sjson.Set(tool, "name", name)
	out, _ = sjson.SetRaw(out, "tools.-1", tool)
	return out
}

// taskFamilySchemas are loose declarations of the other Claude Code task
// tools, added when TaskCreate has worked in the conversation.
var taskFamilySchemas = map[string]string{
	"TaskUpdate": `{"type":"object","properties":{"taskId":{"type":"string","description":"ID of the task to update"},"status":{"type":"string","description":"pending, in_progress, completed or deleted"},"subject":{"type":"string"},"description":{"type":"string"},"activeForm":{"type":"string"}},"required":["taskId"]}`,
	"TaskGet":    `{"type":"object","properties":{"taskId":{"type":"string","description":"ID of the task"}},"required":["taskId"]}`,
	"TaskList":   `{}`,
}

// schemaTypeOf maps a JSON value to a flat JSON Schema type; nested objects
// and arrays are skipped (their shape cannot be reconstructed reliably).
func schemaTypeOf(v gjson.Result) string {
	switch v.Type {
	case gjson.String:
		return "string"
	case gjson.True, gjson.False:
		return "boolean"
	case gjson.Number:
		if v.Float() == float64(int64(v.Float())) {
			return "integer"
		}
		return "number"
	}
	return ""
}

// escapeSJSONKey escapes the characters that are special in an sjson path.
func escapeSJSONKey(k string) string {
	out := make([]byte, 0, len(k))
	for i := 0; i < len(k); i++ {
		switch k[i] {
		case '.', '*', '?', '|', '#', '@', '\\', ':', '!', '=', '<', '>', '%':
			out = append(out, '\\')
		}
		out = append(out, k[i])
	}
	return string(out)
}
