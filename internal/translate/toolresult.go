// Part of the translate package — see translate.go for the package doc
// comment. Direct port of v1's translator.go "tool_result" content-block
// case inside Step 5's message loop.
package translate

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// convertToolResultBlock converts one "tool_result" content block into
// a functionResponse part, plus any image/document parts its content
// carried (returned separately — sealFunctionResponseTurn moves these
// into a following plain-user turn, since Gemini's real FunctionResponse
// message has no "parts" field).
func convertToolResultBlock(p gjson.Result, role string, toolIDToName map[string]string, tn toolNameInfo, root gjson.Result) []string {
	if role != "user" {
		// Anthropic's spec only puts tool_result in a USER turn; a
		// malformed/replayed tool_result inside an ASSISTANT message
		// still unconditionally produced a functionResponse part under
		// "role":"model" — which Gemini rejects outright (functionResponse
		// is only valid in the user/function-response role), 400ing the
		// whole request.
		part := `{"text":""}`
		part, _ = sjson.Set(part, "text", "[tool_result in unexpected role]")
		return []string{part}
	}

	toolUseID := p.Get("tool_use_id").String()
	fnName := toolIDToName[toolUseID]
	if fnName == "" {
		// GAP-V2-06 / GAP-R17-05: If toolUseID cannot be resolved to a declared tool name
		// in the active request (e.g. truncated conversation history), emitting a bogus
		// functionResponse without a matching functionCall causes Gemini HTTP 400.
		// Gracefully degrade the orphan result to plain text.
		c := p.Get("content")
		rawText := c.String()
		if c.IsArray() {
			var sb strings.Builder
			c.ForEach(func(_, item gjson.Result) bool {
				if item.Get("type").String() == "text" {
					sb.WriteString(item.Get("text").String())
				} else if item.Type == gjson.String {
					sb.WriteString(item.String())
				} else if t := item.Get("type").String(); t == "image" || t == "document" {
					sb.WriteString("[binary attachment]")
				} else if txt := blockToText(item); txt != "" {
					sb.WriteString("\n" + txt + "\n")
				}
				return true
			})
			rawText = sb.String()
		}
		if len(rawText) > 500 {
			rawText = cutUTF8(rawText, 500) + "... [truncated]"
		}
		part := `{"text":""}`
		part, _ = sjson.Set(part, "text", fmt.Sprintf("[Prior tool result for %s omitted]: %s", toolUseID, rawText))
		return []string{part}
	}

	resText := ""
	var imageParts []string
	c := p.Get("content")
	if c.IsArray() {
		var sb strings.Builder
		c.ForEach(func(_, item gjson.Result) bool {
			// A tool_result.content array of bare strings (e.g. ["line
			// 1\n", "line 2\n"], not typed {"type":"text",...} blocks) has
			// item.Get("type").String()=="" for every element, matching
			// no case below and silently contributing nothing.
			if item.Type == gjson.String {
				sb.WriteString(item.String())
				return true
			}
			if item.Type == gjson.Number || item.Type == gjson.True || item.Type == gjson.False {
				sb.WriteString(item.Raw)
				return true
			}
			switch item.Get("type").String() {
			case "text":
				sb.WriteString(item.Get("text").String())
			case "image", "document":
				appendAttachmentToToolResult(item, &sb, &imageParts)
			default:
				// GAP-CGN-D2-06 (GAP-D3-14): untyped JSON objects in tool_result array
				if item.IsObject() && item.Get("type").String() == "" {
					sb.WriteString(item.Raw)
				} else if txt := blockToText(item); txt != "" {
					// tool_reference, search_result, ... nested in a
					// tool_result: labelled text, never an empty result.
					if sb.Len() > 0 {
						sb.WriteString("\n")
					}
					sb.WriteString(txt)
				}
			}
			return true
		})
		resText = sb.String()
	} else if c.Exists() {
		resText = c.String()
	}

	// is_error was previously ignored, so a failed tool call looked
	// identical to a successful one to the model — it had no signal to
	// recognize the failure and retry or change approach.
	responseField := "result"
	if p.Get("is_error").Bool() {
		responseField = "error"
	}
	if !tn.effectiveToolNames[fnName] {
		// Same undeclared-tool gap as tool_use above, mirrored for its
		// paired result — a functionResponse naming a tool no longer in
		// this request's functionDeclarations 400s the whole request the
		// same way an orphaned functionCall does.
		resultPreview := resText
		if len(resultPreview) > 200 {
			resultPreview = cutUTF8(resultPreview, 200) + "..."
		}
		// Neutral prose, not bracket notation — see toolcall.go's own
		// equivalent fix for why: a live incident showed the model
		// mimicking a bracketed pseudo-call/result shape from its own
		// history as plain text in a later turn instead of a real
		// structured call.
		part := `{"text":""}`
		part, _ = sjson.Set(part, "text", "(Note: the result of an earlier call to the "+fnName+" tool, "+resultPreview+", is omitted here because that tool is not available in this turn.)")
		return []string{part}
	}

	part := `{"functionResponse":{"name":"","response":{}}}`
	// Same disambiguation-consistency requirement as functionCall.name —
	// fnName here is the raw Claude-side name recovered from toolIDToName.
	responseFnName := tn.disambiguatedToolName[fnName]
	if responseFnName == "" {
		responseFnName = sanitizeGeminiFunctionName(fnName)
	}
	part, _ = sjson.Set(part, "functionResponse.name", responseFnName)
	if toolUseID != "" {
		part, _ = sjson.Set(part, "functionResponse.id", toolUseID)
	}
	// GAP-TL-11: is_error:true with no output text set
	// functionResponse.response.error to "" — the model reads an empty
	// error field as indistinguishable from a successful empty result,
	// with no signal that the call actually failed.
	if responseField == "error" && resText == "" {
		resText = "Tool execution failed (error flag set, no output)."
	}
	if responseField == "result" && gjson.Valid(resText) {
		resParsed := gjson.Parse(resText)
		if resParsed.IsObject() || resParsed.IsArray() {
			part, _ = sjson.SetRaw(part, "functionResponse.response.result", resText)
		} else {
			part, _ = sjson.Set(part, "functionResponse.response."+responseField, resText)
		}
	} else {
		part, _ = sjson.Set(part, "functionResponse.response."+responseField, resText)
	}

	parts := []string{part}
	// Append any images from THIS tool_result as sibling top-level parts
	// (not nested under functionResponse.parts, which real Gemini
	// FunctionResponse messages don't have). sealFunctionResponseTurn
	// sees these as "other" parts mixed into the turn and moves them into
	// a separate following plain "user" turn.
	parts = append(parts, imageParts...)
	return parts
}
