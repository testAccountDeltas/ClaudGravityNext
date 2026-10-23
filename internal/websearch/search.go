// Package websearch is a port of superrelay (v1)'s websearch.go, split
// across four files by responsibility per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 4.3: search.go
// (this file — IsClaudeCodeDirectWebSearch, PerformWebSearch's entry
// point), grounding.go (searchWithGoogleGrounding), fallback.go
// (searchWithPublicFallback/stripHTMLTags), directhandler.go
// (handleClaudeCodeDirectWebSearch/sendSSEEvent).
//
// Two real, documented API changes from v1, both DEPENDENCY INVERSIONS
// discovered by actually attempting this port, not scope creep:
//
//  1. v1's PerformWebSearch/handleClaudeCodeDirectWebSearch took
//     `activeAcc *AccountRecord` and `tenant TenantConfig` — the first is
//     v1's auth_manager.go type (internal/accounts, Phase 2, doesn't
//     exist yet); the second is a verified-dead parameter (read the full
//     v1 function bodies: `tenant` is never referenced anywhere inside
//     either function, just passed through). Replaced `activeAcc` with
//     the two primitive fields these functions actually read
//     (`hasActiveAccount bool`, `projectID string`) and dropped `tenant`
//     entirely — this package now depends on zero account-pool types,
//     which is the right shape regardless of the migration (a websearch
//     helper has no business knowing the account type's full shape).
//  2. v1 reached for the package-level globals `httpClient` (server.go)
//     and `AntigravityUserAgent` (auth_manager.go) directly. Both are now
//     explicit parameters — same reasoning as internal/config's fix to
//     the AntigravityClientID coupling in this same migration phase.
package websearch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"claudgravity/internal/logx"
	"claudgravity/internal/warmup"
)

const claudeCodeWebSearchPrefix = "Perform a web search for the query:"

type WebSearchResultItem struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// IsClaudeCodeDirectWebSearch checks if the incoming request is Claude Code's native search sub-request.
// Claude Code CLI direct web search is strictly single-turn: exactly one tool (web_search)
// and exactly one user message starting with "Perform a web search for the query:".
func IsClaudeCodeDirectWebSearch(bodyBytes []byte) (string, bool) {
	root := gjson.ParseBytes(bodyBytes)

	// Check tools: must declare exactly 1 tool which is web_search
	tools := root.Get("tools")
	if !tools.IsArray() {
		return "", false
	}
	toolList := tools.Array()
	if len(toolList) != 1 {
		return "", false
	}
	t := toolList[0]
	name := t.Get("name").String()
	tType := t.Get("type").String()
	if !strings.EqualFold(name, "web_search") && !strings.HasPrefix(strings.ToLower(tType), "web_search_") {
		return "", false
	}

	// Check messages: must be single-turn (exactly 1 user message)
	messages := root.Get("messages")
	if !messages.IsArray() {
		return "", false
	}
	msgs := messages.Array()
	if len(msgs) != 1 {
		return "", false
	}

	msg := msgs[0]
	if msg.Get("role").String() != "user" {
		return "", false
	}
	content := msg.Get("content")
	if content.Type == gjson.String {
		txt := strings.TrimSpace(content.String())
		if strings.HasPrefix(txt, claudeCodeWebSearchPrefix) {
			q := strings.TrimSpace(strings.TrimPrefix(txt, claudeCodeWebSearchPrefix))
			return q, true
		}
	} else if content.IsArray() {
		for _, part := range content.Array() {
			if part.Get("type").String() == "text" {
				txt := strings.TrimSpace(part.Get("text").String())
				if strings.HasPrefix(txt, claudeCodeWebSearchPrefix) {
					q := strings.TrimSpace(strings.TrimPrefix(txt, claudeCodeWebSearchPrefix))
					return q, true
				}
			}
		}
	}

	return "", false
}

func newServerToolUseID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "srvtoolu_" + hex.EncodeToString(b)
}

// PerformWebSearch executes the search via Google CloudCode Grounding
// (primary) or Wikipedia/DuckDuckGo (fallback). See this file's doc
// comment for why hasActiveAccount/projectID/httpClient/antigravityUserAgent
// replace v1's *AccountRecord and package-level globals.
func PerformWebSearch(
	ctx context.Context,
	query string,
	googleToken string,
	isAntigravity bool,
	hasActiveAccount bool,
	projectID string,
	httpClient *http.Client,
	antigravityUserAgent string,
) ([]WebSearchResultItem, string, error) {
	// 1. Try Google Search Grounding with gemini-2.5-flash
	if googleToken != "" && isAntigravity && hasActiveAccount {
		results, summary, err := searchWithGoogleGrounding(ctx, query, googleToken, projectID, httpClient, antigravityUserAgent)
		if err == nil && len(results) > 0 {
			return results, summary, nil
		}
		logx.LogGateway(".. Google Search Grounding fallback: %v", err)
	}

	// 2. Fallback: DuckDuckGo Instant Answer / Wikipedia
	return searchWithPublicFallback(ctx, query, httpClient)
}

// antigravityInferenceHosts is the same host pool warmup.MainInferenceHosts
// uses for the main inference path — reused here rather than duplicated,
// per the single-ownership rule (Part 4.2 of the spec).
var antigravityInferenceHosts = warmup.MainInferenceHosts
