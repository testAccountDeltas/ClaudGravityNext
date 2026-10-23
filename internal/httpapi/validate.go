// Part of the httpapi package — see errors.go for the package doc
// comment. Request-body validation for POST /v1/messages — a faithful
// port of v1's handleMessages section "1. Resolve Tenant ... validation".
// Treated as a real correctness/security boundary (malformed input must
// be rejected before any account-selection round-trip is spent on it),
// not casually rewritten.
package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// validateMessagesRequest checks the hard-400 cases Anthropic's own spec
// requires but which would otherwise sail through to Gemini and burn a
// real upstream round-trip before failing: malformed JSON, missing/empty
// "messages", missing/empty "model", missing/non-positive "max_tokens",
// and the two tool_choice misuses translator.go/internal/translate
// silently swallow rather than reject (tool_choice with no "tools"
// declared at all, and tool_choice naming a tool that isn't in "tools").
// Returns ("", true) when valid, or the error message plus false.
func validateMessagesRequest(bodyBytes []byte) (errMsg string, ok bool) {
	if !gjson.ValidBytes(bodyBytes) {
		return "Request body must be valid JSON", false
	}
	if !gjson.GetBytes(bodyBytes, "messages").IsArray() || len(gjson.GetBytes(bodyBytes, "messages").Array()) == 0 {
		return "\"messages\": array is required and must be non-empty", false
	}
	if gjson.GetBytes(bodyBytes, "model").String() == "" {
		return "\"model\": required field is missing", false
	}
	if mt := gjson.GetBytes(bodyBytes, "max_tokens"); !mt.Exists() || mt.Type != gjson.Number || mt.Int() < 0 || float64(mt.Int()) != mt.Num {
		return "\"max_tokens\": required field must be a non-negative integer", false
	}
	if tc := gjson.GetBytes(bodyBytes, "tool_choice"); tc.Exists() {
		toolsArr := gjson.GetBytes(bodyBytes, "tools")
		if !toolsArr.IsArray() || len(toolsArr.Array()) == 0 {
			return "\"tool_choice\": provided without any \"tools\" declared", false
		}
		// GAP-TL-10: tool_choice can be a bare JSON string ("auto"/
		// "any"/"none"), not just {"type":"..."}  — tc.Get("type") on a
		// string value has no sub-path and returns "", which this
		// validation used to reject as invalid even though the
		// shorthand is legal.
		choiceType := tc.Get("type").String()
		if tc.Type == gjson.String {
			choiceType = tc.String()
		}
		switch choiceType {
		case "auto", "any", "required", "tool", "none":
		default:
			return fmt.Sprintf("\"tool_choice.type\": %q is not one of auto, any, tool, none", choiceType), false
		}
		if choiceType == "tool" {
			wantName := tc.Get("name").String()
			found := false
			for _, t := range toolsArr.Array() {
				tName := t.Get("name").String()
				toolType := t.Get("type").String()
				if tName == "" && toolType != "" {
					if strings.HasPrefix(toolType, "text_editor_") {
						tName = "str_replace_editor"
					} else {
						tName = strings.TrimRight(strings.TrimRightFunc(toolType, func(r rune) bool {
							return r >= '0' && r <= '9'
						}), "_")
						if tName == "" {
							tName = toolType
						}
					}
				}
				if tName == wantName || toolType == wantName || (strings.HasPrefix(toolType, "text_editor_") && (wantName == "text_editor" || wantName == "str_replace_editor")) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Sprintf("\"tool_choice\": tool %q is not present in \"tools\"", wantName), false
			}
		}
	}
	// GAP-P2-02: Anthropic's own spec rejects these two thinking
	// misconfigurations with a 400 before ever reaching inference —
	// nothing in this gateway validated either, letting a malformed
	// request sail through to generation.go's own best-effort
	// adjustment (which silently widens max_tokens/lowers budget to
	// make the Gemini call valid, rather than telling the client its
	// request was invalid in the first place). Scoped to
	// thinking.type=="enabled" only: "adaptive" (the current default for
	// 4.6+ models) never carries budget_tokens at all, and "disabled"
	// doesn't either.
	if th := gjson.GetBytes(bodyBytes, "thinking"); th.Exists() {
		// GAP-D1-15: an unrecognized/typo'd thinking.type used to sail
		// through silently (the budget checks below only ever gated on
		// the literal string "enabled"), with no 400 telling the client
		// its request was malformed.
		switch th.Get("type").String() {
		case "enabled", "disabled", "adaptive", "between_tools":
		default:
			return fmt.Sprintf("\"thinking.type\": %q is not one of enabled, disabled, adaptive, between_tools", th.Get("type").String()), false
		}
	}
	if th := gjson.GetBytes(bodyBytes, "thinking"); th.Exists() && th.Get("type").String() == "enabled" {
		bt := th.Get("budget_tokens")
		if !bt.Exists() {
			return "\"thinking.budget_tokens\": required when \"thinking.type\" is \"enabled\"", false
		}
		if bt.Int() < 1024 {
			return fmt.Sprintf("\"thinking.budget_tokens\": %d is below the minimum of 1024", bt.Int()), false
		}
		if mt := gjson.GetBytes(bodyBytes, "max_tokens"); mt.Exists() && mt.Int() > 0 && mt.Int() <= bt.Int() {
			return fmt.Sprintf("\"max_tokens\": %d must be greater than \"thinking.budget_tokens\" (%d)", mt.Int(), bt.Int()), false
		}
	}
	return "", true
}

// maxRequestBodyBytes is Anthropic's documented request-body cap: an
// unbounded io.ReadAll on a client sending a huge binary payload is a
// memory-exhaustion risk, not just a spec gap.
const maxRequestBodyBytes = 25 * 1024 * 1024

// maxInlineDataBytes is Gemini's real inlineData payload limit — tighter
// than maxRequestBodyBytes, so a 20-25MB request passes the body-size
// check above but would still 400 upstream once translated without this
// separate, earlier, cleaner rejection.
const maxInlineDataBytes = 20 * 1024 * 1024

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	WriteAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "Method Not Allowed")
}
