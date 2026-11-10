package translate

import (
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
	"claudgravity/internal/toolsig"
)

// A past function call whose own signature is unknown to the gateway (it was
// restarted, the cache expired, or the gateway made the call itself) must be
// signed the same way on every later turn. Signing it with "the latest
// signature of the session" changed the call's part each turn, and with it
// the whole prompt behind it, which is what stopped Gemini's implicit cache
// from matching.
func TestPastCallWithUnknownSignature_IsSignedIdenticallyEveryTurn(t *testing.T) {
	const session = "prefix-stability-session"
	req := func() string {
		return `{"model":"claude-opus-4-5","max_tokens":64,"tools":[{"name":"Read","description":"r","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}],` +
			`"messages":[{"role":"user","content":"go"},` +
			`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_unknown_to_the_gateway","name":"Read","input":{"file_path":"a.go"}}]},` +
			`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_unknown_to_the_gateway","content":"package a"}]},` +
			`{"role":"assistant","content":[{"type":"text","text":"ok"}]},` +
			`{"role":"user","content":"next"}]}`
	}
	convert := func() string {
		res, err := ConvertClaudeRequestToGemini([]byte(req()), config.TenantConfig{}, "main", "main", session, "", true)
		if err != nil {
			t.Fatal(err)
		}
		return gjson.GetBytes(res.GeminiBody, "contents.1").Raw
	}
	toolsig.SetSessionSignature(session, "signature-after-turn-6", "")
	first := convert()
	toolsig.SetSessionSignature(session, "signature-after-turn-7", "")
	second := convert()
	if first != second {
		t.Fatalf("the past call changed between turns:\n%s\n%s", first, second)
	}
	if gjson.Get(first, "parts.0.thoughtSignature").String() != "skip_thought_signature_validator" {
		t.Fatalf("expected the deterministic bypass signature: %s", first)
	}
}

// A call the gateway DOES know keeps its real signature.
func TestPastCallWithKnownSignature_KeepsIt(t *testing.T) {
	toolsig.Set("toolu_known_sig_stability", "real-signature-xyz", "")
	block := gjson.Parse(`{"type":"tool_use","id":"toolu_known_sig_stability","name":"Read","input":{}}`)
	tn := toolNameInfo{effectiveToolNames: map[string]bool{"Read": true}, disambiguatedToolName: map[string]string{}}
	parts := convertToolUseBlock(block, "assistant", tn, true, gjson.Parse(`[]`), "any-session")
	if len(parts) != 1 || gjson.Get(parts[0], "thoughtSignature").String() != "real-signature-xyz" {
		t.Fatalf("got %v", parts)
	}
}
