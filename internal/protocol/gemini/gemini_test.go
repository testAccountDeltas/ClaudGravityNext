// Tests for the new gemini protocol domain — see
// internal/protocol/claude's test file comment for why this new
// foundational code gets direct tests, not a v1-characterization test.
package gemini

import (
	"encoding/json"
	"testing"
)

func TestPart_FunctionCallRoundTrip(t *testing.T) {
	raw := []byte(`{"functionCall":{"id":"call1","name":"Read","args":{"file_path":"x.go"}}}`)
	var p Part
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.FunctionCall == nil || p.FunctionCall.Name != "Read" {
		t.Fatalf("got %+v", p)
	}
	out, _ := json.Marshal(p)
	var p2 Part
	_ = json.Unmarshal(out, &p2)
	if p2.FunctionCall == nil || p2.FunctionCall.Name != "Read" {
		t.Fatalf("round-trip failed, got %+v", p2)
	}
	if p.Text != "" || p.InlineData != nil {
		t.Fatal("other Part fields must stay zero-valued when only functionCall is present")
	}
}

func TestPart_TextWithThoughtSignature(t *testing.T) {
	raw := []byte(`{"text":"reasoning...","thought":true,"thoughtSignature":"sig123"}`)
	var p Part
	_ = json.Unmarshal(raw, &p)
	if !p.Thought || p.ThoughtSignature != "sig123" || p.Text != "reasoning..." {
		t.Fatalf("got %+v", p)
	}
}

func TestGenerateContentRequest_FullRoundTrip(t *testing.T) {
	raw := []byte(`{
		"contents": [{"role": "user", "parts": [{"text": "hi"}]}],
		"tools": [{"functionDeclarations": [{"name": "Read", "parameters": {"type": "object"}}]}],
		"toolConfig": {"functionCallingConfig": {"mode": "ANY", "allowedFunctionNames": ["Read"]}},
		"generationConfig": {"maxOutputTokens": 4096, "thinkingConfig": {"thinkingBudget": -1}}
	}`)
	var req GenerateContentRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(req.Contents) != 1 || req.Contents[0].Parts[0].Text != "hi" {
		t.Fatalf("got contents=%+v", req.Contents)
	}
	if req.ToolConfig.FunctionCallingConfig.Mode != "ANY" {
		t.Fatalf("got toolConfig=%+v", req.ToolConfig)
	}
	if req.GenerationConfig.ThinkingConfig.ThinkingBudget != -1 {
		t.Fatalf("got generationConfig=%+v", req.GenerationConfig)
	}
}

func TestGenerateContentResponse_GroundingMetadataDecodes(t *testing.T) {
	raw := []byte(`{
		"candidates": [{
			"content": {"role": "model", "parts": [{"text": "answer"}]},
			"finishReason": "STOP",
			"groundingMetadata": {
				"groundingChunks": [{"web": {"uri": "https://example.com", "title": "Example"}}],
				"groundingSupports": [{"segment": {"text": "answer"}, "groundingChunkIndices": [0]}]
			}
		}],
		"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 5}
	}`)
	var resp GenerateContentResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Candidates) != 1 || resp.Candidates[0].FinishReason != FinishStop {
		t.Fatalf("got %+v", resp.Candidates)
	}
	gm := resp.Candidates[0].GroundingMetadata
	if gm == nil || len(gm.GroundingChunks) != 1 || gm.GroundingChunks[0].Web.URI != "https://example.com" {
		t.Fatalf("got groundingMetadata=%+v", gm)
	}
	if resp.UsageMetadata.PromptTokenCount != 10 {
		t.Fatalf("got usage=%+v", resp.UsageMetadata)
	}
}
