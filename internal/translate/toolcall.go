// Part of the translate package — see translate.go for the package doc
// comment. Direct port of v1's translator.go "tool_use" content-block
// case inside Step 5's message loop.
package translate

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/toolargs"
	"claudgravity/internal/toolsig"
)

// convertToolUseBlock converts one "tool_use" content block into a
// single Gemini part — a functionCall, or a degraded text summary when
// the enclosing role is wrong or the tool isn't actually declared to
// Gemini this request.
func convertToolUseBlock(p gjson.Result, role string, tn toolNameInfo, isAntigravity bool, opt ...interface{}) []string {
	var content gjson.Result
	var sessionID string
	if len(opt) > 0 {
		if c, ok := opt[0].(gjson.Result); ok {
			content = c
		}
	}
	if len(opt) > 1 {
		if s, ok := opt[1].(string); ok {
			sessionID = s
		}
	}
	name := p.Get("name").String()
	callID := p.Get("id").String()
	argsRaw := p.Get("input").Raw
	if argsRaw == "" || argsRaw == "null" {
		// gjson's .Raw for a JSON `null` value is the literal string
		// "null", not "" — a tool_use block with "input": null passed
		// straight through as "args": null, which Gemini rejects with
		// 400 since functionCall.args must be an object.
		argsRaw = "{}"
	}
	// sse.go's own outbound responses already run newly-generated tool_use
	// args through the same normalization, but history containing a
	// non-object input (a client-supplied turn, or an older capture) needs
	// it applied here too, on replay.
	argsRaw = toolargs.NormalizeToArgsObject(name, argsRaw)

	// Prefer the real signature this exact call was originally emitted
	// with (cached when first sent, by internal/stream) over the
	// skip-validator bypass. If toolsig has no cached signature for callID,
	// look for a sibling "thinking" block in this same assistant turn which
	// carries the real signature echoed by Claude Code, or fall back to
	// the session-level signature, avoiding conflicting signatures within the turn.
	sig := toolsig.Get(callID)
	if sig == "" && content.IsArray() {
		content.ForEach(func(_, sibling gjson.Result) bool {
			if sibling.Get("type").String() == "thinking" {
				if s := sibling.Get("signature").String(); s != "" && s != "skip_thought_signature_validator" {
					sig = s
					return false
				}
			}
			return true
		})
	}
	if sig == "" && sessionID != "" {
		sig = toolsig.GetSessionSignature(sessionID)
	}
	if sig == "" {
		sig = "skip_thought_signature_validator"
	}

	if role != "assistant" {
		// Anthropic's own spec only ever puts tool_use in an assistant
		// turn, but a malformed/replayed/third-party client sending one
		// inside a USER message still produces "role":"user" with a
		// functionCall part, which Gemini's protobuf validator rejects
		// outright ("functionCall is only supported for model role"),
		// 400ing the WHOLE request instead of just degrading this one odd
		// block.
		part := `{"text":""}`
		part, _ = sjson.Set(part, "text", "[tool_use in unexpected role: "+name+"]")
		return []string{part}
	}

	if !tn.effectiveToolNames[name] {
		// A tool this request no longer declares to Gemini at all — most
		// commonly Grep/Glob under the default strip_search_tools, used in
		// an EARLIER turn before being stripped from every later turn's
		// functionDeclarations — must not be replayed as a functionCall
		// referencing an undeclared name: Gemini validates every
		// functionCall.name in the conversation against the CURRENT
		// declared list and 400s the WHOLE request otherwise.
		argsPreview := argsRaw
		if len(argsPreview) > 200 {
			argsPreview = argsPreview[:200] + "..."
		}
		// Deliberately NOT formatted as "[Called Name(args)]" — a real,
		// observed incident (live traffic capture) showed the model
		// pattern-matching that exact bracket-and-parens shape from its
		// own history and reproducing it VERBATIM as plain text for an
		// unrelated later tool call, instead of emitting a real
		// structured tool_use (stop_reason stayed "end_turn", so Claude
		// Code never executed it — the call silently became dead text in
		// the user's terminal). Neutral prose carries the same
		// information to the model without looking like callable syntax
		// worth imitating.
		part := `{"text":""}`
		if isAntigravity {
			part, _ = sjson.Set(part, "thoughtSignature", "skip_thought_signature_validator")
		}
		part, _ = sjson.Set(part, "text", "(Note: an earlier call to the "+name+" tool, with arguments "+argsPreview+", is omitted here because that tool is not available in this turn.)")
		return []string{part}
	}

	part := `{"functionCall":{"name":"","args":{}}}`
	// Gemini attaches the thought signature to the FIRST functionCall of a
	// parallel group only (ai.google.dev "thought signatures"); replaying
	// the same signature on every sibling is not what the model produced.
	if isAntigravity && isFirstReplayedCall(p, content, tn) {
		part, _ = sjson.Set(part, "thoughtSignature", sig)
	}
	// disambiguatedToolName (computed once, up front) must be used so a
	// collision-disambiguated name ("docker_run_1") matches exactly what
	// the tools declaration sent — falling back to plain sanitization
	// only for a tool_use whose name isn't in the CURRENT request's
	// declared tools at all (e.g. truncated-out history).
	callFnName := tn.disambiguatedToolName[name]
	if callFnName == "" {
		callFnName = sanitizeGeminiFunctionName(name)
	}
	part, _ = sjson.Set(part, "functionCall.name", callFnName)
	if callID != "" {
		part, _ = sjson.Set(part, "functionCall.id", callID)
	}
	part, _ = sjson.SetRaw(part, "functionCall.args", argsRaw)
	return []string{part}
}

// isFirstReplayedCall reports whether p is the first tool_use block of its
// assistant message that is replayed as a real functionCall (a tool this
// request no longer declares is replayed as prose and does not count). A
// message without sibling context (content not an array) is its own group.
func isFirstReplayedCall(p gjson.Result, content gjson.Result, tn toolNameInfo) bool {
	if !content.IsArray() {
		return true
	}
	first := true
	found := false
	content.ForEach(func(_, b gjson.Result) bool {
		if b.Get("type").String() != "tool_use" || !tn.effectiveToolNames[b.Get("name").String()] {
			return true
		}
		found = true
		first = b.Raw == p.Raw || (p.Get("id").String() != "" && b.Get("id").String() == p.Get("id").String())
		return false
	})
	return first || !found
}
