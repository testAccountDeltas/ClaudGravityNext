package tiering

import "strings"

// auxiliaryMarkers are system-prompt fragments of Claude Code's own
// background helpers (session title, topic detection, summaries, ...).
var auxiliaryMarkers = []string{
	"generate a concise title",
	"sentence-case title",
	"sentence case title",
	"3-5 word title",
	"summarize this coding session",
	"summarize the conversation",
	"summarise the conversation",
	"detect new conversation topic",
	"isnewtopic",
	"analyze if this message indicates a new conversation topic",
	"extract any file paths",
	"classify whether",
	"classifier",
}

// ClassifyRequestClass returns the effective request class. An explicit
// x-claude-code-request-class value always wins. Without it (hint headers
// are optional: CLAUDE_CODE_GATEWAY_HINT_HEADERS), small tool-less requests
// that look like title/summary/topic/classifier helpers are classified
// "auxiliary" so they land on the cheap tier.
func ClassifyRequestClass(reqClass, model string, maxTokens int64, hasTools bool, systemText string) string {
	if reqClass != "" {
		return reqClass
	}
	if hasTools || maxTokens <= 0 {
		return reqClass
	}
	sys := strings.ToLower(systemText)
	marker := false
	for _, m := range auxiliaryMarkers {
		if strings.Contains(sys, m) {
			marker = true
			break
		}
	}
	haiku := ModelFamily(NormalizeModelID(model)) == "haiku"
	switch {
	case maxTokens <= 64:
		return "auxiliary"
	case marker && maxTokens <= 512:
		return "auxiliary"
	case haiku && marker:
		return "auxiliary"
	}
	return reqClass
}
