// Part of the httpapi package — see errors.go for the package doc
// comment. Measures the translated request's inlineData payload.
package httpapi

import "github.com/tidwall/gjson"

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
