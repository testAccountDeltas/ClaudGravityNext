// Part of the translate package — see translate.go for the package doc
// comment. Faithful text rendering of Anthropic content-block types that
// have no Gemini counterpart (search_result, mcp_*, code-execution
// results, tool_reference, compaction, ...). Previously these were
// dropped and the message collapsed to a "..." placeholder, silently
// destroying the content the model was supposed to see.
package translate

import (
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// blockTextMaxBytes bounds one rendered block so a huge search/exec result
// cannot blow up the prompt; the cut is rune-safe and marked.
const blockTextMaxBytes = 16000

func truncateBlockText(s string) string {
	if len(s) <= blockTextMaxBytes {
		return s
	}
	cut := blockTextMaxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n... [truncated]"
}

// blockToText renders one non-text, non-media content block (a top-level
// message block or an item nested in a tool_result) as concise labelled
// text. Returns "" only when the block carries nothing at all.
func blockToText(b gjson.Result) string {
	if b.Type == gjson.String {
		return b.String()
	}
	if !b.IsObject() {
		return b.Raw
	}
	typ := b.Get("type").String()
	var out string
	switch typ {
	case "text":
		out = b.Get("text").String()
	case "tool_reference":
		out = "[tool: " + b.Get("tool_name").String() + "]"
	case "search_result":
		out = renderSearchResult(b)
	case "compaction":
		out = "[Conversation summary]\n" + contentToText(b.Get("content"))
	case "mcp_tool_use":
		out = "[MCP tool call"
		if s := b.Get("server_name").String(); s != "" {
			out += " " + s + "."
		} else {
			out += " "
		}
		out += b.Get("name").String()
		if in := b.Get("input").Raw; in != "" && in != "{}" {
			out += " input: " + in
		}
		out += "]"
	case "mcp_tool_result":
		label := "[MCP tool result"
		if b.Get("is_error").Bool() {
			label += " (error)"
		}
		out = label + "]\n" + contentToText(b.Get("content"))
	case "web_search_tool_result":
		out = "[Web search results]\n" + contentToText(b.Get("content"))
	case "web_search_result":
		out = "- " + b.Get("title").String() + " (" + b.Get("url").String() + ")"
		if sn := b.Get("snippet").String(); sn != "" {
			out += ": " + sn
		}
	case "server_tool_use":
		out = "[Executed server tool " + b.Get("name").String()
		if in := b.Get("input").Raw; in != "" && in != "{}" {
			out += " with input: " + in
		}
		out += "]"
	default:
		if strings.HasSuffix(typ, "_tool_result") { // code_execution, bash_code_execution, ...
			out = "[" + strings.ReplaceAll(strings.TrimSuffix(typ, "_tool_result"), "_", " ") + " result]\n" + contentToText(b.Get("content"))
		} else if typ == "code_execution_result" || typ == "bash_code_execution_result" {
			out = execResultText(b)
		} else if typ != "" {
			body := b.Raw
			if c := b.Get("content"); c.Exists() {
				body = contentToText(c)
			}
			out = "[" + typ + "] " + body
		} else {
			out = b.Raw
		}
	}
	return truncateBlockText(strings.TrimSpace(out))
}

func renderSearchResult(b gjson.Result) string {
	var sb strings.Builder
	sb.WriteString("[Search result")
	if t := b.Get("title").String(); t != "" {
		sb.WriteString(": " + t)
	}
	sb.WriteString("]")
	if s := b.Get("source").String(); s != "" {
		sb.WriteString("\nSource: " + s)
	}
	if c := contentToText(b.Get("content")); c != "" {
		sb.WriteString("\n" + c)
	}
	return sb.String()
}

func execResultText(b gjson.Result) string {
	var sb strings.Builder
	if v := b.Get("stdout").String(); v != "" {
		sb.WriteString("stdout:\n" + v + "\n")
	}
	if v := b.Get("stderr").String(); v != "" {
		sb.WriteString("stderr:\n" + v + "\n")
	}
	if rc := b.Get("return_code"); rc.Exists() {
		sb.WriteString("return_code: " + rc.Raw + "\n")
	}
	if sb.Len() == 0 {
		return b.Raw
	}
	return sb.String()
}

// contentToText flattens a block's "content" (string, array of blocks, or
// object) into text, recursing through blockToText for typed items.
func contentToText(c gjson.Result) string {
	switch {
	case !c.Exists() || c.Type == gjson.Null:
		return ""
	case c.Type == gjson.String:
		return c.String()
	case c.IsArray():
		var parts []string
		c.ForEach(func(_, it gjson.Result) bool {
			if s := blockToText(it); s != "" {
				parts = append(parts, s)
			}
			return true
		})
		return strings.Join(parts, "\n")
	case c.IsObject():
		if c.Get("type").Exists() {
			return blockToText(c)
		}
		if c.Get("stdout").Exists() || c.Get("stderr").Exists() {
			return execResultText(c)
		}
		return c.Raw
	}
	return c.Raw
}

// textPart wraps s as a Gemini {"text":...} part.
func textPart(s string) string {
	part, _ := sjson.Set(`{"text":""}`, "text", s)
	return part
}
