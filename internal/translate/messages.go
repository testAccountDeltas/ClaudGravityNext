// Part of the translate package — see translate.go for the package doc
// comment. Direct port of v1's translator.go Step 5 (Messages
// Translation): the main per-message loop building Gemini's "contents"
// array, dispatching each Claude content block to the per-type
// converters in toolcall.go/toolresult.go/thinking.go/images.go.
package translate

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// translateMessages appends root's "messages" onto out's "contents"
// array, in Gemini's Content{Role,Parts} shape.
func translateMessages(out string, root gjson.Result, tn toolNameInfo, isAntigravity bool, sessionID, targetModel string) string {
	toolIDToName := make(map[string]string)
	messages := root.Get("messages")
	if !messages.IsArray() {
		return out
	}

	// Google's own equivalent agy.exe converter independently corroborates
	// Anthropic's own extended-thinking documentation: only the single
	// most recent assistant message's thinking needs preserving for
	// continuity within an ACTIVE tool-use loop; resending every
	// historical thought on every request just accumulates context bloat.
	lastAssistantIdx := -1
	msgArr := messages.Array()
	for i := len(msgArr) - 1; i >= 0; i-- {
		if msgArr[i].Get("role").String() == "assistant" {
			lastAssistantIdx = i
			break
		}
	}

	contentIdx := 0
	var lastRole string

	messages.ForEach(func(msgIdxResult, msg gjson.Result) bool {
		role := msg.Get("role").String()
		geminiRole := "user"
		if role == "assistant" {
			geminiRole = "model"
		}
		isLastAssistantMsg := role == "assistant" && int(msgIdxResult.Int()) == lastAssistantIdx

		// Pre-pass: map tool_use IDs to function names. Scoped to
		// role=="assistant": a tool_use sitting in a role-inverted USER
		// message still degrades to plain text below and never becomes a
		// real functionCall, so registering its id here would let a
		// later tool_result for that same id find a non-empty fnName and
		// emit a real functionResponse with no matching functionCall
		// ever sent — an orphaned response Gemini rejects. server_tool_use
		// is excluded because it degrades to text and never emits functionCall.
		if parts := msg.Get("content"); parts.IsArray() {
			parts.ForEach(func(_, p gjson.Result) bool {
				ptype := p.Get("type").String()
				if ptype == "tool_use" && role == "assistant" {
					id := p.Get("id").String()
					name := p.Get("name").String()
					if id != "" && name != "" {
						toolIDToName[id] = name
					}
				}
				return true
			})
		}

		var partsJSON []string

		if content := msg.Get("content"); content.IsArray() {
			content.ForEach(func(_, p gjson.Result) bool {
				ptype := p.Get("type").String()
				switch ptype {
				case "text":
					txt := p.Get("text").String()
					if strings.TrimSpace(txt) != "" {
						part := `{"text":""}`
						part, _ = sjson.Set(part, "text", txt)
						partsJSON = append(partsJSON, part)
					}
				case "image", "document":
					partsJSON = append(partsJSON, convertImageDocumentBlock(ptype, p, role)...)
				case "tool_use":
					partsJSON = append(partsJSON, convertToolUseBlock(p, role, tn, isAntigravity, content, sessionID, targetModel)...)
				case "server_tool_use":
					// In a MODEL turn this is a call real Claude made (the
					// conversation was moved to Gemini). A bracketed
					// "[Executed server tool ...]" text part there is what
					// Gemini copies back as its own output: drop it.
					if role != "assistant" {
						partsJSON = append(partsJSON, convertServerToolUseBlock(p)...)
					}
				case "tool_result":
					partsJSON = append(partsJSON, convertToolResultBlock(p, role, toolIDToName, tn, root)...)
				case "web_search_tool_result":
					if role != "assistant" { // see server_tool_use above
						partsJSON = append(partsJSON, convertWebSearchToolResultBlock(p)...)
					}
				case "thinking":
					partsJSON = append(partsJSON, convertThinkingBlock(p, role, isLastAssistantMsg, content, isAntigravity, sessionID, targetModel)...)
				case "redacted_thinking":
					partsJSON = append(partsJSON, convertRedactedThinkingBlock(role, isLastAssistantMsg, isAntigravity)...)
				default:
					// search_result, mcp_*, code-execution results,
					// compaction, ... : faithful labelled text instead of
					// silently dropping the block (see blocktext.go).
					if ptype != "" {
						if txt := blockToText(p); txt != "" {
							partsJSON = append(partsJSON, textPart(txt))
						}
					}
				}
				return true
			})
		} else if content.Type == gjson.String {
			txt := content.String()
			if strings.TrimSpace(txt) != "" {
				part := `{"text":""}`
				part, _ = sjson.Set(part, "text", txt)
				partsJSON = append(partsJSON, part)
			}
		}

		if len(partsJSON) == 0 {
			part := `{"text":"..."}`
			if geminiRole == "model" && isAntigravity {
				part, _ = sjson.Set(part, "thoughtSignature", "skip_thought_signature_validator")
			}
			partsJSON = append(partsJSON, part)
		}
		partsJSON = sealFunctionResponseTurn(partsJSON)

		if geminiRole == lastRole && contentIdx > 0 {
			targetIdx := contentIdx - 1
			// This message's own parts are already pure (sealed above),
			// but merging them into the PREVIOUS turn can still produce a
			// mix if that turn is pure functionResponse and this one is
			// pure text (or vice versa), both the same Gemini role — e.g.
			// a tool_result-only turn immediately followed by a separate
			// plain-text user turn. Re-seal the combined set.
			existingRaw := gjson.Get(out, fmt.Sprintf("contents.%d.parts", targetIdx))
			var combined []string
			existingRaw.ForEach(func(_, v gjson.Result) bool {
				combined = append(combined, v.Raw)
				return true
			})
			combined = append(combined, partsJSON...)
			combined = sealFunctionResponseTurn(combined)
			if geminiRole == "model" {
				combined = reorderThoughtPartsFirst(combined)
			}
			combinedJSON := "[" + strings.Join(combined, ",") + "]"
			out, _ = sjson.SetRaw(out, fmt.Sprintf("contents.%d.parts", targetIdx), combinedJSON)
		} else {
			if geminiRole == "model" {
				partsJSON = reorderThoughtPartsFirst(partsJSON)
			}
			out, _ = sjson.Set(out, fmt.Sprintf("contents.%d.role", contentIdx), geminiRole)
			for _, pjson := range partsJSON {
				out, _ = sjson.SetRaw(out, fmt.Sprintf("contents.%d.parts.-1", contentIdx), pjson)
			}
			lastRole = geminiRole
			contentIdx++
		}

		return true
	})

	return out
}

// reorderThoughtPartsFirst moves every "thought":true part ahead of
// everything else, preserving relative order within each group — Google
// Antigravity strictly enforces that all thought parts must precede any
// other parts in a model turn, even after merging consecutive model
// turns.
func reorderThoughtPartsFirst(parts []string) []string {
	var thoughtParts, otherParts []string
	for _, pjson := range parts {
		if gjson.Get(pjson, "thought").Bool() {
			thoughtParts = append(thoughtParts, pjson)
		} else {
			otherParts = append(otherParts, pjson)
		}
	}
	return append(thoughtParts, otherParts...)
}

// convertWebSearchToolResultBlock formats web_search_tool_result content into
// a clean markdown summary so Gemini preserves the search context on replay.
func convertWebSearchToolResultBlock(p gjson.Result) []string {
	c := p.Get("content")
	var sb strings.Builder
	sb.WriteString("[Web Search Results]:\n")
	if c.IsArray() {
		c.ForEach(func(_, item gjson.Result) bool {
			if item.Get("type").String() == "web_search_result" {
				title := item.Get("title").String()
				url := item.Get("url").String()
				snippet := item.Get("snippet").String()
				sb.WriteString(fmt.Sprintf("- %s (%s): %s\n", title, url, snippet))
			} else if item.Type == gjson.String {
				sb.WriteString(item.String() + "\n")
			} else {
				sb.WriteString(item.Raw + "\n")
			}
			return true
		})
	} else if c.Exists() {
		sb.WriteString(c.String())
	}
	res := strings.TrimSpace(sb.String())
	if res == "" || res == "[Web Search Results]:" {
		res = "[Web Search Results: none]"
	}
	part := `{"text":""}`
	part, _ = sjson.Set(part, "text", res)
	return []string{part}
}

// convertServerToolUseBlock converts a server_tool_use block (e.g. server-side
// web search or code execution emitted by Claude) into an informative text part.
// Crucially, it must NOT become a Gemini functionCall part: under Gemini's
// Protobuf API, every functionCall MUST be followed by a matching functionResponse.
// Because server tools are executed server-side, the client never sends a
// tool_result, which would cause Gemini to reject the turn with HTTP 400.
func convertServerToolUseBlock(p gjson.Result) []string {
	name := p.Get("name").String()
	input := p.Get("input").Raw
	var desc string
	if name != "" {
		if input != "" && input != "{}" {
			desc = fmt.Sprintf("[Executed server tool %s with input: %s]", name, input)
		} else {
			desc = fmt.Sprintf("[Executed server tool %s]", name)
		}
	} else {
		desc = "[Executed server tool]"
	}
	part := `{"text":""}`
	part, _ = sjson.Set(part, "text", desc)
	return []string{part}
}
