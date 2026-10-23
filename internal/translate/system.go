// Part of the translate package — see translate.go for the package doc
// comment. Direct port of v1's translator.go Step 4 (system instruction
// assembly).
package translate

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/loopguard"
)

// applySystemInstruction scrubs and assembles root's "system" field into
// out's system_instruction.parts, folding in a disable_parallel_tool_use nudge
// when the client asked for it, and appending any pending LoopGuard notices
// as system_instruction parts rather than polluting tool_result bodies.
func applySystemInstruction(out string, root gjson.Result, sessionKeys ...string) string {
	systemPrompt := ""
	if sys := root.Get("system"); sys.Exists() {
		if sys.IsArray() {
			var sb strings.Builder
			sys.ForEach(func(_, block gjson.Result) bool {
				txt := block.Get("text").String()
				if txt == "" && block.Type == gjson.String {
					txt = block.String()
				}
				if cleaned := ScrubBillingHeader(txt); cleaned != "" {
					sb.WriteString(cleaned)
					sb.WriteString("\n")
				}
				return true
			})
			systemPrompt = strings.TrimSpace(sb.String())
		} else if sys.Type == gjson.String {
			systemPrompt = ScrubBillingHeader(sys.String())
		}
	}
	// disable_parallel_tool_use is completely unhandled by Gemini itself —
	// it frequently returns multiple functionCall parts in one candidate
	// regardless, so without this a client that set the flag (expecting
	// at most one tool_use per turn, per Anthropic's own contract)
	// silently got several anyway. The actual enforcement (dropping any
	// call past the first) belongs to the future internal/stream caller —
	// this system instruction is a best-effort nudge to stop Gemini from
	// generating the extra calls in the first place. Official Anthropic
	// SDKs actually send this nested inside tool_choice
	// (`tool_choice: {type:"auto", disable_parallel_tool_use:true}`), not
	// as a top-level request field — check both.
	if root.Get("disable_parallel_tool_use").Bool() || root.Get("tool_choice.disable_parallel_tool_use").Bool() {
		systemPrompt = strings.TrimSpace(systemPrompt + "\n\nYou must call at most ONE tool in this response. Do not perform parallel/simultaneous tool calls.")
	}
	if systemPrompt != "" {
		// Per Google's REST spec, system_instruction is just {"parts":
		// [...]} — role is only meaningful on conversation turns in
		// "contents", not here.
		out, _ = sjson.Set(out, "system_instruction.parts.0.text", systemPrompt)
	}

	// Consume and inject pending loop notices into system_instruction parts
	// rather than polluting tool_result bodies in conversation contents.
	var allNudges []string
	seenNudge := make(map[string]bool)
	for _, key := range sessionKeys {
		if key == "" {
			continue
		}
		for _, nudge := range loopguard.TakePendingNudges(key) {
			if !seenNudge[nudge] {
				seenNudge[nudge] = true
				allNudges = append(allNudges, nudge)
			}
		}
	}
	for _, nudge := range allNudges {
		out, _ = sjson.Set(out, "system_instruction.parts.-1.text", nudge)
	}

	return out
}
