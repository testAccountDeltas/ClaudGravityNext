// Part of the translate package — see translate.go for the package doc
// comment. Direct port of v1's ScrubBillingHeader (translator.go).
package translate

import (
	"regexp"
	"strings"
)

// currentLocalTimeMinuteSecondRe zeroes the minutes/seconds of an ISO 8601
// timestamp (the "current local time: 2026-10-17T11:56:07+03:00" line
// Claude Code puts in its "# Environment" block), quantizing it to the
// hour. Keeping the line verbatim kept the model honestly date/time-aware
// but changed the system prompt's bytes every single second, invalidating
// Google's byte-prefix cache on every turn; quantizing to the hour keeps
// the model's sense of "now" accurate enough for any real use (git log
// date filtering, "is it business hours") while the system prompt — and
// the cache built on it — stays byte-stable for 3600 seconds at a time
// instead of 1.
var currentLocalTimeMinuteSecondRe = regexp.MustCompile(`(T\d{2}):\d{2}:\d{2}`)

// claudeCodeSelfIDRe matches only the self-identification clause itself —
// the real line continues straight on with real behavioral instructions
// ("Given the user's message, you should use the tools available to
// complete the task. Complete the task fully...") that a whole-line
// replacement used to discard along with it.
var claudeCodeSelfIDRe = regexp.MustCompile(`(?i)you are (an agent for )?claude code, anthropic's official cli( for claude)?\.?`)

var builtOnAnthropicSDKRe = regexp.MustCompile(`(?i)\s*built on anthropic['’]s claude agent sdk`)
var builtOnAnthropicRe = regexp.MustCompile(`(?i)\s*built on anthropic(['’]s)?`)
var claudeAgentSDKRe = regexp.MustCompile(`(?i)claude agent sdk`)
var strayPossessiveAssistantRe = regexp.MustCompile(`(?i)['’]s\s+(a\s+)?coding assistant`)

// ScrubBillingHeader strips out fingerprint headers and billing marks that
// trigger Google 429/Filter:
//  1. x-anthropic-billing-header
//  2. Canonical Claude Code self-identification preambles that Google
//     CloudCode actively filters
//  3. Environmental noise (gitStatus, Recent commits, fast mode tags) to
//     reduce prompt tokens
func ScrubBillingHeader(txt string) string {
	var cleanLines []string
	skipSection := false
	skipSectionLines := 0

	for _, line := range strings.Split(txt, "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)

		// Filter billing header marks
		if strings.Contains(line, "x-anthropic-billing-header") {
			continue
		}
		if strings.Contains(lower, "claude agent sdk") ||
			strings.Contains(lower, "built on anthropic") ||
			strayPossessiveAssistantRe.MatchString(line) {
			// Same class of bug as the "you are claude code" self-ID fix
			// below: replacing the WHOLE line risked discarding real
			// trailing instructions that continue past the identifying
			// phrase on the same line. Replace "built on Anthropic's Claude Agent SDK"
			// and strip "built on Anthropic['s]" cleanly without leaving
			// dangling possessive artifacts like "'s a coding assistant".
			scrubbed := builtOnAnthropicSDKRe.ReplaceAllString(line, " a coding assistant")
			scrubbed = builtOnAnthropicRe.ReplaceAllString(scrubbed, "")
			scrubbed = claudeAgentSDKRe.ReplaceAllString(scrubbed, "a coding assistant")
			scrubbed = strayPossessiveAssistantRe.ReplaceAllString(scrubbed, "a coding assistant")
			cleanLines = append(cleanLines, scrubbed)
			continue
		}

		// Filter heavy repetitive environment sections — but the section
		// also carries the model's only signal for which OS/shell it's
		// actually running on (Platform/Shell/working directory). Dropping
		// it wholesale previously left Gemini guessing, and it would guess
		// Unix (export, ls, grep) on a Windows box as often as not.
		if trimmed == "# Environment" {
			skipSection = true
			skipSectionLines = 0
			cleanLines = append(cleanLines, line)
			continue
		}
		if skipSection {
			skipSectionLines++
			// Any heading level ends the section, not just another
			// top-level "# " one — a "## Guidelines"/"### Rules"
			// subsection nested under "# Environment" (or following it
			// before the next real top-level heading) matched neither
			// "# " nor the Environment whitelist below, so it and
			// everything after it kept getting silently dropped as if it
			// were still environment noise, sometimes eating the rest of
			// the system prompt. But "# Environment" can also be the LAST
			// heading in the real prompt entirely — no heading of any
			// level ever follows it — in which case that fix alone still
			// ate everything to the end of the prompt (model description,
			// skills catalog, date). The known Environment fields
			// (platform/shell/os/working directory/git-repo/scratchpad)
			// comfortably fit well under 20 lines; past that, whatever
			// follows isn't this section anymore even without a heading
			// to prove it.
			if strings.HasPrefix(trimmed, "#") || skipSectionLines > 20 {
				skipSection = false
			} else {
				if strings.Contains(lower, "platform:") ||
					strings.Contains(lower, "shell:") ||
					strings.Contains(lower, "os version:") ||
					strings.Contains(lower, "working directory:") ||
					// Dropped silently like every other unlisted
					// Environment line — but these two are load-bearing:
					// without "is a git repository" Gemini doesn't know
					// git commands are even on the table, and without
					// "scratchpad directory" it has no designated place
					// for temp files and starts littering the repo root
					// or /tmp instead.
					strings.Contains(lower, "is a git repository:") ||
					strings.Contains(lower, "scratchpad directory:") ||
					// Dropping this line left Gemini with no idea what the
					// real current date/year is, so it would default to
					// whatever's baked into its training data — guessing
					// 2023/2024, misdating git log filters, and generally
					// reasoning about "now" incorrectly.
					strings.Contains(lower, "current local time") {
					cleanLines = append(cleanLines, currentLocalTimeMinuteSecondRe.ReplaceAllString(line, "${1}:00:00"))
				}
				continue
			}
		}

		// Strip fast-mode noise markers only — gitStatus:/Recent commits:
		// used to be stripped too, but that section is only ~80-180 tokens
		// and removing it meant Gemini had no idea which branch/files were
		// already changed, so its very first move was burning a Bash
		// round-trip on "git status"/"git log" to ask for information that
		// was already sitting right there — a turn's worth of latency and
		// tool_use/tool_result tokens to "save" a sliver of context.
		if strings.HasPrefix(trimmed, "<fast_mode_info>") ||
			strings.HasPrefix(trimmed, "</fast_mode_info>") {
			continue
		}

		// Normalize self-identification — replace only the identifying
		// clause, keeping whatever real behavioral instructions follow it
		// on the same line.
		if strings.Contains(lower, "claude code, anthropic's official cli") {
			cleanLines = append(cleanLines, claudeCodeSelfIDRe.ReplaceAllString(line, "You are a helpful coding assistant."))
			continue
		}

		cleanLines = append(cleanLines, line)
	}
	return strings.TrimSpace(strings.Join(cleanLines, "\n"))
}
