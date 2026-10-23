// Part of the httpapi package — see errors.go for the package doc
// comment. estimateInlineDataBytes — a faithful port of v1's function of
// the same name, used by messages.go's pre-translation 20MB check.
package httpapi

import (
	"strings"

	"github.com/tidwall/gjson"
)

// estimateInlineDataBytes sums the byte length of every base64 "data"
// string that will actually become a Gemini inlineData part: image or
// document content blocks (at the top level of a message, or nested
// inside a tool_result's own content array) whose source.type is
// "base64", mirroring internal/translate's own routing condition exactly
// — a base64 TEXT document (CSV, code, logs) decodes to a plain text
// part and never touches Gemini's real inlineData limit at all, and an
// assistant-role image/document block gets swapped for a short text
// placeholder (Gemini's model role only allows text/thought/
// functionCall), so neither should count toward a limit it structurally
// can't touch.
func estimateInlineDataBytes(root gjson.Result) int {
	total := 0
	var walkBlocks func(blocks gjson.Result, role string)
	walkBlocks = func(blocks gjson.Result, role string) {
		if !blocks.IsArray() {
			return
		}
		blocks.ForEach(func(_, block gjson.Result) bool {
			ptype := block.Get("type").String()
			switch ptype {
			case "image", "document":
				src := block.Get("source")
				if src.Get("type").String() == "base64" {
					mediaType := strings.ToLower(src.Get("media_type").String())
					if idx := strings.Index(mediaType, ";"); idx != -1 {
						mediaType = strings.TrimSpace(mediaType[:idx])
					}
					if mediaType == "" {
						if ptype == "document" {
							mediaType = "application/pdf"
						} else {
							mediaType = "image/png"
						}
					}
					isBinary := mediaType != "image/svg+xml" && (mediaType == "application/pdf" || strings.HasPrefix(mediaType, "image/"))
					if isBinary && role != "assistant" {
						total += len(src.Get("data").String())
					}
				}
			case "tool_result":
				c := block.Get("content")
				if c.IsArray() {
					// tool_result is always carried in a USER turn per the
					// Anthropic spec, but pass the outer role through
					// unchanged rather than hardcoding "user" — a
					// role-inverted tool_result (rejected elsewhere as a
					// separate edge case) shouldn't silently start
					// counting bytes it also can't actually reach
					// inlineData with.
					walkBlocks(c, role)
				}
			}
			return true
		})
	}
	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		walkBlocks(msg.Get("content"), msg.Get("role").String())
		return true
	})
	return total
}

// estimateGeminiInlineDataBytes measures the total inlineData bytes in a
// translated Gemini request body (contents[].parts[].inlineData.data),
// ensuring the 20MB check runs against post-truncation payloads so old
// attachments pruned by context truncation don't cause permanent session 413s.
func estimateGeminiInlineDataBytes(geminiRoot gjson.Result) int {
	total := 0
	geminiRoot.Get("contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if id := part.Get("inlineData.data"); id.Exists() {
				total += len(id.String())
			}
			return true
		})
		return true
	})
	return total
}
