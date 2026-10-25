// Part of the translate package — see translate.go for the package doc comment.
//
// Claude Code shows what every background agent is doing as a short label. It
// produces the label by FORKING the agent's whole conversation (same tools, so
// the prompt cache is shared) and appending an instruction that ends in "Do not
// use tools." Claude answers with a few words; Gemini, after a tool result,
// habitually calls the next tool instead (about 85% of the fork requests in a
// measured five-agent audit, a dozen of them a multi-thousand-token
// SubagentHandback report), the client throws such a reply away, and the label
// stays on the last successful one — "Reading translator.go ..." for half an
// hour. The gateway enforces the instruction itself: the tools stay declared
// (keeping the cached prefix valid) but the model may not call them.
package translate

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// agentSummaryMarker is the opening of the instruction Claude Code appends.
const agentSummaryMarker = "Describe your most recent action in 3-5 words using present tense"

// isAgentSummaryFork reports whether the newest user message carries the
// background-agent label instruction.
func isAgentSummaryFork(root gjson.Result) bool {
	msgs := root.Get("messages").Array()
	if len(msgs) == 0 {
		return false
	}
	last := msgs[len(msgs)-1]
	if last.Get("role").String() != "user" {
		return false
	}
	content := last.Get("content")
	if content.Type == gjson.String {
		return strings.Contains(content.String(), agentSummaryMarker)
	}
	found := false
	content.ForEach(func(_, b gjson.Result) bool {
		if b.Get("type").String() == "text" && strings.Contains(b.Get("text").String(), agentSummaryMarker) {
			found = true
			return false
		}
		return true
	})
	return found
}

// forbidToolCalls switches function calling off in out while leaving the tool
// declarations alone.
func forbidToolCalls(out string) string {
	out, _ = sjson.Delete(out, "toolConfig.functionCallingConfig.allowedFunctionNames")
	out, _ = sjson.Set(out, "toolConfig.functionCallingConfig.mode", "NONE")
	return out
}
