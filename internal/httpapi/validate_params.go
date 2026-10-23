// Part of the httpapi package — see validate.go. Sampling-parameter, role
// and stop_sequences validation, each mapped to Anthropic's 400
// invalid_request_error instead of reaching Gemini (which would either
// reject it with an opaque error or silently clamp it).
package httpapi

import (
	"fmt"

	"github.com/tidwall/gjson"
)

// validateRequestShape returns a message for the first shape violation.
// Roles: "user" and "assistant" are the API's; "system" is additionally
// tolerated because internal/sanitize folds it (OpenAI-style clients), any
// other role is a 400. Message order is NOT checked (a conversation may
// start with an assistant turn).
func validateRequestShape(body []byte) (string, bool) {
	for i, m := range gjson.GetBytes(body, "messages").Array() {
		if !m.IsObject() {
			return fmt.Sprintf("messages.%d: each message must be an object", i), false
		}
		role := m.Get("role")
		if role.Type != gjson.String {
			return fmt.Sprintf("messages.%d.role: Field required", i), false
		}
		switch role.String() {
		case "user", "assistant", "system":
		default:
			return fmt.Sprintf("messages.%d.role: Input should be 'user' or 'assistant', got %q", i, role.String()), false
		}
	}
	if t := gjson.GetBytes(body, "temperature"); t.Exists() && t.Type != gjson.Null {
		if t.Type != gjson.Number || t.Num < 0 || t.Num > 1 {
			return "\"temperature\": must be a number between 0 and 1", false
		}
	}
	if p := gjson.GetBytes(body, "top_p"); p.Exists() && p.Type != gjson.Null {
		if p.Type != gjson.Number || p.Num < 0 || p.Num > 1 {
			return "\"top_p\": must be a number between 0 and 1", false
		}
	}
	if k := gjson.GetBytes(body, "top_k"); k.Exists() && k.Type != gjson.Null {
		if k.Type != gjson.Number || k.Num < 0 || float64(k.Int()) != k.Num {
			return "\"top_k\": must be an integer >= 0", false
		}
	}
	// stop_sequences: any count is accepted (translate forwards the first 5,
	// Gemini's cap, and logs the rest); only the element type is validated.
	if ss := gjson.GetBytes(body, "stop_sequences"); ss.Exists() && ss.Type != gjson.Null {
		if !ss.IsArray() {
			return "\"stop_sequences\": must be an array of strings", false
		}
		for i, s := range ss.Array() {
			if s.Type != gjson.String {
				return fmt.Sprintf("\"stop_sequences.%d\": must be a string", i), false
			}
		}
	}
	return "", true
}
