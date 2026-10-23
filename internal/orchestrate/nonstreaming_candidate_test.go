package orchestrate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/reqctx"
)

func newClaudeResp() map[string]interface{} {
	return map[string]interface{}{"content": []map[string]interface{}{}}
}

func TestAppendThinkingBlock_CoalescesMultipleThoughtParts(t *testing.T) {
	resp := newClaudeResp()
	appendThinkingBlock(resp, "first ", "sig1")
	appendThinkingBlock(resp, "second", "")

	blocks := resp["content"].([]map[string]interface{})
	if len(blocks) != 1 {
		t.Fatalf("expected Gemini's split thought parts to coalesce into exactly ONE thinking block (Anthropic allows at most one), got %d", len(blocks))
	}
	if blocks[0]["thinking"] != "first second" {
		t.Fatalf("expected concatenated thinking text, got %q", blocks[0]["thinking"])
	}
	if blocks[0]["signature"] != "sig1" {
		t.Fatalf("expected the first real signature to be kept, got %q", blocks[0]["signature"])
	}
}

func TestAppendThinkingBlock_RealSignatureReplacesSkipPlaceholder(t *testing.T) {
	resp := newClaudeResp()
	appendThinkingBlock(resp, "text", "") // no signature yet -> "skip_thought_signature_validator"
	appendThinkingBlock(resp, "", "real-sig")

	blocks := resp["content"].([]map[string]interface{})
	if blocks[0]["signature"] != "real-sig" {
		t.Fatalf("expected a later real signature to replace the skip-validator placeholder, got %q", blocks[0]["signature"])
	}
}

func TestAppendThinkingBlock_InsertedBeforeExistingContent(t *testing.T) {
	resp := map[string]interface{}{"content": []map[string]interface{}{
		{"type": "text", "text": "already here"},
	}}
	appendThinkingBlock(resp, "reasoning", "sig")
	blocks := resp["content"].([]map[string]interface{})
	if len(blocks) != 2 || blocks[0]["type"] != "thinking" {
		t.Fatal("expected the thinking block to be inserted FIRST, per Anthropic's content-order requirement")
	}
}

func TestProcessCandidate_ToolUseDropsTrailingTextBlock(t *testing.T) {
	// Anthropic requires tool_use to be the LAST content block whenever
	// stop_reason ends up "tool_use" — a strict client reading
	// content[len-1] as the tool call breaks if a text block follows it.
	candJSON := `{"content":{"parts":[
		{"functionCall":{"name":"Read","args":{"file_path":"a.go"}}},
		{"text":"trailing narration"}
	]}}`
	cand := gjson.Parse(candJSON)
	resp := newClaudeResp()
	p := &reqctx.Params{}
	cr := processCandidate(p, cand, resp, []byte(`{}`), claude.Request{}, "sess")

	if len(cr.toolsCalled) != 1 || cr.toolsCalled[0] != "Read" {
		t.Fatalf("expected exactly one tool call recorded, got %v", cr.toolsCalled)
	}
	blocks := resp["content"].([]map[string]interface{})
	if len(blocks) != 1 || blocks[0]["type"] != "tool_use" {
		t.Fatalf("expected the trailing text part to be dropped, leaving only the tool_use block, got %v", blocks)
	}
}

func TestProcessCandidate_DisableParallelToolUseDropsSecondCall(t *testing.T) {
	candJSON := `{"content":{"parts":[
		{"functionCall":{"name":"Read","args":{"file_path":"a.go"}}},
		{"functionCall":{"name":"Read","args":{"file_path":"b.go"}}}
	]}}`
	cand := gjson.Parse(candJSON)
	resp := newClaudeResp()
	p := &reqctx.Params{}
	claudeBody := []byte(`{"disable_parallel_tool_use":true}`)
	cr := processCandidate(p, cand, resp, claudeBody, claude.Request{}, "sess")

	if len(cr.toolsCalled) != 1 {
		t.Fatalf("expected the second tool call to be dropped under disable_parallel_tool_use, got %v", cr.toolsCalled)
	}
}

func TestProcessCandidate_ReverseToolNameMapRestoresOriginalName(t *testing.T) {
	candJSON := `{"content":{"parts":[{"functionCall":{"name":"docker_run","args":{}}}]}}`
	cand := gjson.Parse(candJSON)
	resp := newClaudeResp()
	p := &reqctx.Params{ReverseToolNameMap: map[string]string{"docker_run": "docker:run"}}
	cr := processCandidate(p, cand, resp, []byte(`{}`), claude.Request{}, "sess")

	if len(cr.toolsCalled) != 1 || cr.toolsCalled[0] != "docker:run" {
		t.Fatalf("expected the Gemini-sanitized name to be restored to the original Claude-side tool name, got %v", cr.toolsCalled)
	}
}

func TestProcessCandidate_NoCandidateReturnsZeroValue(t *testing.T) {
	resp := newClaudeResp()
	p := &reqctx.Params{}
	cr := processCandidate(p, gjson.Result{}, resp, []byte(`{}`), claude.Request{}, "sess")
	if cr.textChars != 0 || len(cr.toolsCalled) != 0 {
		t.Fatal("expected a zero-value result for a genuinely missing candidate")
	}
}

func TestProcessCandidate_CoalescesConsecutiveTextParts(t *testing.T) {
	candJSON := `{"content":{"parts":[
		{"text":"Hello, "},
		{"text":"world!"}
	]}}`
	cand := gjson.Parse(candJSON)
	resp := newClaudeResp()
	p := &reqctx.Params{}
	cr := processCandidate(p, cand, resp, []byte(`{}`), claude.Request{}, "sess")

	blocks := resp["content"].([]map[string]interface{})
	if len(blocks) != 1 {
		t.Fatalf("expected consecutive text parts to coalesce into exactly ONE text block, got %d", len(blocks))
	}
	if blocks[0]["text"] != "Hello, world!" {
		t.Fatalf("expected coalesced text 'Hello, world!', got %q", blocks[0]["text"])
	}
	if cr.textChars != len("Hello, world!") {
		t.Fatalf("expected cr.textChars %d, got %d", len("Hello, world!"), cr.textChars)
	}
}

func TestProcessCandidate_CoalescesGroundingSourcesWithExistingText(t *testing.T) {
	candJSON := `{"content":{"parts":[
		{"text":"According to sources:"}
	]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://example.com","title":"Example"}}]}}`
	cand := gjson.Parse(candJSON)
	resp := newClaudeResp()
	p := &reqctx.Params{}
	cr := processCandidate(p, cand, resp, []byte(`{}`), claude.Request{}, "sess")

	blocks := resp["content"].([]map[string]interface{})
	if len(blocks) != 1 {
		t.Fatalf("expected grounding sources to coalesce onto the existing text block, got %d blocks", len(blocks))
	}
	text := blocks[0]["text"].(string)
	if !strings.HasPrefix(text, "According to sources:") || !strings.Contains(text, "Google Search Sources") {
		t.Fatalf("expected text prefix and grounding sources in coalesced block, got %q", text)
	}
	if len(cr.toolsCalled) != 1 || cr.toolsCalled[0] != "googleSearch" {
		t.Fatalf("expected googleSearch recorded in toolsCalled, got %v", cr.toolsCalled)
	}
}

func TestProcessCandidate_GroundingSourcesWithoutPrecedingTextCreatesTextBlock(t *testing.T) {
	candJSON := `{"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://example.com","title":"Example"}}]}}`
	cand := gjson.Parse(candJSON)
	resp := newClaudeResp()
	p := &reqctx.Params{}
	cr := processCandidate(p, cand, resp, []byte(`{}`), claude.Request{}, "sess")

	blocks := resp["content"].([]map[string]interface{})
	if len(blocks) != 1 || blocks[0]["type"] != "text" {
		t.Fatalf("expected standalone text block for grounding sources when no prior text, got %v", blocks)
	}
	if !strings.Contains(blocks[0]["text"].(string), "Google Search Sources") {
		t.Fatalf("expected grounding sources header, got %q", blocks[0]["text"])
	}
	if len(cr.toolsCalled) != 1 || cr.toolsCalled[0] != "googleSearch" {
		t.Fatalf("expected googleSearch recorded in toolsCalled, got %v", cr.toolsCalled)
	}
}

func TestProcessCandidate_ThinkingFollowedByMultipleTextBlocksCoalesced(t *testing.T) {
	candJSON := `{"content":{"parts":[
			{"thought":true,"text":"Thinking step...","thoughtSignature":"sig-123"},
			{"text":"Part 1. "},
			{"text":"Part 2."}
		]}}`
	cand := gjson.Parse(candJSON)
	resp := newClaudeResp()
	p := &reqctx.Params{}
	cr := processCandidate(p, cand, resp, []byte(`{}`), claude.Request{}, "sess")

	blocks := resp["content"].([]map[string]interface{})
	if len(blocks) != 2 {
		t.Fatalf("expected exactly 2 blocks (1 thinking, 1 coalesced text), got %d", len(blocks))
	}
	if blocks[0]["type"] != "thinking" || blocks[0]["thinking"] != "Thinking step..." {
		t.Fatalf("unexpected thinking block: %v", blocks[0])
	}
	if blocks[1]["type"] != "text" || blocks[1]["text"] != "Part 1. Part 2." {
		t.Fatalf("unexpected coalesced text block: %v", blocks[1])
	}
	if cr.textChars != len("Part 1. Part 2.") {
		t.Fatalf("expected cr.textChars %d, got %d", len("Part 1. Part 2."), cr.textChars)
	}
}

func TestProcessCandidate_NoGroundingFooterAfterMaxTokensOrRefusal(t *testing.T) {
	for _, fr := range []string{"MAX_TOKENS", "SAFETY"} {
		candJSON := `{"finishReason":"` + fr + `","content":{"parts":[{"text":"partial"}]},"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://example.com","title":"Example"}}]}}`
		resp := newClaudeResp()
		processCandidate(&reqctx.Params{}, gjson.Parse(candJSON), resp, []byte(`{}`), claude.Request{}, "sess")
		blocks := resp["content"].([]map[string]interface{})
		if txt := blocks[0]["text"].(string); strings.Contains(txt, "Sources") {
			t.Fatalf("%s: footer must not be appended, got %q", fr, txt)
		}
	}
}
