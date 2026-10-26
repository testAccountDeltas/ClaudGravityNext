// Part of the orchestrate package. A request with max_tokens: 0 is Anthropic's
// documented cache pre-warm: the prompt is read (and cached at the
// cache_control breakpoints), nothing is generated, and the response has an
// empty content array, stop_reason "max_tokens" and output_tokens 0. The
// upstream call still runs with a one-token allowance (generation.go), which
// is what warms Gemini's own cache; the token it produced is discarded here.
package orchestrate

import "github.com/tidwall/gjson"

// applyPrewarmShape rewrites a finished non-streaming response into the
// pre-warm shape when the request asked for max_tokens: 0.
func applyPrewarmShape(claudeResp map[string]interface{}, requestBody []byte) {
	mt := gjson.GetBytes(requestBody, "max_tokens")
	if !mt.Exists() || mt.Int() != 0 {
		return
	}
	claudeResp["content"] = []map[string]interface{}{}
	claudeResp["stop_reason"] = "max_tokens"
	claudeResp["stop_sequence"] = nil
	if usage, ok := claudeResp["usage"].(map[string]interface{}); ok {
		usage["output_tokens"] = 0
	}
}
