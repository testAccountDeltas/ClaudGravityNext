// Phase 1 characterization test for the ported websearch package —
// proves IsClaudeCodeDirectWebSearch's prefix/tool detection,
// stripHTMLTags, and PerformWebSearch's fallback-on-no-account routing
// behave as in v1.
package websearch

import (
	"context"
	"net/http"
	"testing"
)

func TestIsClaudeCodeDirectWebSearch_RequiresToolAndPrefix(t *testing.T) {
	body := []byte(`{
		"tools": [{"name": "web_search", "type": "web_search_20250305"}],
		"messages": [{"role": "user", "content": "Perform a web search for the query: golang generics"}]
	}`)
	q, ok := IsClaudeCodeDirectWebSearch(body)
	if !ok || q != "golang generics" {
		t.Fatalf("expected match with query %q, got ok=%v q=%q", "golang generics", ok, q)
	}
}

func TestIsClaudeCodeDirectWebSearch_NoToolNoMatch(t *testing.T) {
	body := []byte(`{
		"tools": [{"name": "Bash"}],
		"messages": [{"role": "user", "content": "Perform a web search for the query: golang generics"}]
	}`)
	if _, ok := IsClaudeCodeDirectWebSearch(body); ok {
		t.Fatal("must not match without a web_search tool present, regardless of message prefix")
	}
}

func TestIsClaudeCodeDirectWebSearch_WrongPrefixNoMatch(t *testing.T) {
	body := []byte(`{
		"tools": [{"name": "web_search"}],
		"messages": [{"role": "user", "content": "just a normal question"}]
	}`)
	if _, ok := IsClaudeCodeDirectWebSearch(body); ok {
		t.Fatal("must not match when the last user message doesn't start with the exact Claude Code search prefix")
	}
}

func TestIsClaudeCodeDirectWebSearch_MultiTurnNoMatch(t *testing.T) {
	body := []byte(`{
		"tools": [{"name": "web_search"}],
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": "hi"},
			{"role": "user", "content": "Perform a web search for the query: golang generics"}
		]
	}`)
	if _, ok := IsClaudeCodeDirectWebSearch(body); ok {
		t.Fatal("must not match in multi-turn conversations even if the last message has the search prefix")
	}
}

func TestIsClaudeCodeDirectWebSearch_MultipleToolsNoMatch(t *testing.T) {
	body := []byte(`{
		"tools": [{"name": "web_search"}, {"name": "Bash"}],
		"messages": [{"role": "user", "content": "Perform a web search for the query: golang generics"}]
	}`)
	if _, ok := IsClaudeCodeDirectWebSearch(body); ok {
		t.Fatal("must not match when multiple tools are declared")
	}
}

func TestIsClaudeCodeDirectWebSearch_ArrayContentBlock(t *testing.T) {
	body := []byte(`{
		"tools": [{"type": "web_search_20250305"}],
		"messages": [{"role": "user", "content": [{"type": "text", "text": "Perform a web search for the query: rust async"}]}]
	}`)
	q, ok := IsClaudeCodeDirectWebSearch(body)
	if !ok || q != "rust async" {
		t.Fatalf("expected array-content-block match with query %q, got ok=%v q=%q", "rust async", ok, q)
	}
}

func TestStripHTMLTags_RemovesTagsKeepsText(t *testing.T) {
	got := stripHTMLTags("<span class=\"x\">hello <b>world</b></span>")
	if got != "hello world" {
		t.Fatalf("expected tags stripped and text preserved, got %q", got)
	}
}

func TestPerformWebSearch_NoAccountSkipsGroundingGoesToFallback(t *testing.T) {
	// hasActiveAccount=false must skip the grounding branch entirely and
	// never dereference anything account-shaped — the whole point of the
	// dependency-inversion fix (search.go's doc comment). The public
	// fallback does real network I/O (Wikipedia), so just prove it
	// doesn't panic and returns at least the minimal last-resort result.
	results, summary, err := PerformWebSearch(context.Background(), "test query", "", false, false, "", http.DefaultClient, "")
	if err != nil {
		t.Fatalf("PerformWebSearch with no account must still return the public fallback, not an error: %v", err)
	}
	if len(results) == 0 || summary == "" {
		t.Fatal("expected at least the minimal last-resort fallback result")
	}
}
