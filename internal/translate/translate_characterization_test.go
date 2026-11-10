// Characterization tests for internal/translate against v1's
// translator.go. One test per audit-caught edge case documented in that
// file's own comments.
package translate

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
	"claudgravity/internal/loopguard"
	"claudgravity/internal/toolsig"
)

func mustJSON(t *testing.T, raw string) string {
	t.Helper()
	if !gjson.Valid(raw) {
		t.Fatalf("invalid JSON literal in test: %s", raw)
	}
	return raw
}

// --- sealing.go ---

func TestSealFunctionResponseTurn_TextFoldedIntoUserNote(t *testing.T) {
	parts := []string{
		`{"functionResponse":{"name":"Read","response":{"result":"file contents"}}}`,
		`{"text":"please also check this"}`,
	}
	sealed := sealFunctionResponseTurn(parts)
	if len(sealed) != 1 {
		t.Fatalf("expected exactly 1 sealed part, got %d: %v", len(sealed), sealed)
	}
	note := gjson.Get(sealed[0], "functionResponse.response.user_note").String()
	if note != "please also check this" {
		t.Fatalf("expected user_note to carry the text, got %q", note)
	}
}

func TestSealFunctionResponseTurn_RepeatedMergeConcatenatesNotOverwrites(t *testing.T) {
	// GAP-D3-16: a growing combined parts list, across repeated merge
	// rounds, must concatenate onto an EXISTING user_note from an earlier
	// round rather than overwrite it.
	firstRound := sealFunctionResponseTurn([]string{
		`{"functionResponse":{"name":"Read","response":{"result":"x"}}}`,
		`{"text":"round one"}`,
	})
	secondRoundInput := append(firstRound, `{"text":"round two"}`)
	sealed := sealFunctionResponseTurn(secondRoundInput)
	note := gjson.Get(sealed[0], "functionResponse.response.user_note").String()
	if note != "round one\nround two" {
		t.Fatalf("expected both rounds concatenated, got %q", note)
	}
}

func TestSealFunctionResponseTurn_ImagesStayInSameTurnAfterFunctionResponse(t *testing.T) {
	parts := []string{
		`{"functionResponse":{"name":"Read","response":{"result":"ok"}}}`,
		`{"inlineData":{"mimeType":"image/png","data":"AAAA"}}`,
	}
	sealed := sealFunctionResponseTurn(parts)
	if len(sealed) != 2 {
		t.Fatalf("expected functionResponse + image in one turn, got %d", len(sealed))
	}
	if gjson.Get(sealed[0], "functionResponse.parts").Exists() {
		t.Fatalf("real Gemini FunctionResponse has no parts field — image must not nest there")
	}
	if !gjson.Get(sealed[0], "functionResponse").Exists() || !gjson.Get(sealed[1], "inlineData").Exists() {
		t.Fatalf("functionResponse must come first, image after: %v", sealed)
	}
}

func TestSealFunctionResponseTurn_AllSameKindIsNoOp(t *testing.T) {
	parts := []string{`{"text":"a"}`, `{"text":"b"}`}
	sealed := sealFunctionResponseTurn(parts)
	if len(sealed) != 2 {
		t.Fatalf("pure-text turn must pass through unchanged, got %v", sealed)
	}
}

// --- toolnames.go ---

func TestSanitizeGeminiFunctionName_HyphenatedMCPName(t *testing.T) {
	got := sanitizeGeminiFunctionName("mcp__brave-search__search")
	if got != "mcp__brave_search__search" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitizeGeminiFunctionName_LeadingDigit(t *testing.T) {
	got := sanitizeGeminiFunctionName("123tool")
	if !strings.HasPrefix(got, "_") {
		t.Fatalf("expected a leading underscore for a digit-starting name, got %q", got)
	}
}

func TestComputeToolNameInfo_CollisionDisambiguation(t *testing.T) {
	root := gjson.Parse(mustJSON(t, `{"tools":[{"name":"docker:run"},{"name":"docker_run"}]}`))
	tn := computeToolNameInfo(root, false)
	a := tn.disambiguatedToolName["docker:run"]
	b := tn.disambiguatedToolName["docker_run"]
	if a == b {
		t.Fatalf("two distinct original names must not collide on the same sanitized name, got %q for both", a)
	}
	// "docker:run" is declared first and claims the clean "docker_run"
	// sanitization; "docker_run" (declared second, already literally
	// that string) collides with it and gets the suffixed slot instead.
	if a != "docker_run" || b != "docker_run_1" {
		t.Fatalf("expected docker:run->docker_run and docker_run->docker_run_1, got %q and %q", a, b)
	}
}

func TestComputeToolNameInfo_ForcedAnyKeepsSearchToolsWhenAllAreSearchTools(t *testing.T) {
	root := gjson.Parse(mustJSON(t, `{"tool_choice":{"type":"any"},"tools":[{"name":"Grep"},{"name":"Glob"}]}`))
	tn := computeToolNameInfo(root, true)
	if !tn.effectiveToolNames["Grep"] || !tn.effectiveToolNames["Glob"] {
		t.Fatalf("forced tool_choice:any with ONLY search tools declared must suppress strip_search_tools entirely, got %+v", tn.effectiveToolNames)
	}
}

func TestComputeToolNameInfo_StripSearchToolsDropsGrepGlobByDefault(t *testing.T) {
	root := gjson.Parse(mustJSON(t, `{"tools":[{"name":"Grep"},{"name":"Read"}]}`))
	tn := computeToolNameInfo(root, true)
	if tn.effectiveToolNames["Grep"] {
		t.Fatalf("Grep must be stripped when strip_search_tools is on and no forced-any exemption applies")
	}
	if !tn.effectiveToolNames["Read"] {
		t.Fatalf("Read must survive untouched")
	}
}

// --- images.go ---

func TestConvertImageDocumentBlock_SVGExcludedFromBinaryPlaceholder(t *testing.T) {
	// SVG is XML text, not binary raster data — an assistant-authored SVG
	// must survive as real text, not get replaced by a one-line
	// "[Assistant output image: ...]" placeholder the way genuine binary
	// images do in the model role.
	block := gjson.Parse(mustJSON(t, `{"type":"image","source":{"type":"base64","media_type":"image/svg+xml","data":"PHN2Zz48L3N2Zz4="}}`))
	parts := convertImageDocumentBlock("image", block, "assistant")
	if len(parts) != 1 {
		t.Fatalf("expected exactly 1 part, got %d: %v", len(parts), parts)
	}
	if gjson.Get(parts[0], "inlineData").Exists() {
		t.Fatalf("SVG must never become inlineData")
	}
	text := gjson.Get(parts[0], "text").String()
	if !strings.Contains(text, "<svg>") {
		t.Fatalf("expected the decoded SVG text itself, got %q", text)
	}
}

func TestConvertImageDocumentBlock_AllSVGVariantsNeverInlineData(t *testing.T) {
	svgTypes := []string{
		"image/svg+xml",
		"image/svg",
		"image/x-svg",
		"image/svg-xml",
		"IMAGE/SVG+XML; charset=utf-8",
		"image/svg; foo=bar",
	}
	for _, mt := range svgTypes {
		// User role: must decode to plain text part, NEVER inlineData (which Gemini rejects with HTTP 400)
		blockJSON := fmt.Sprintf(`{"type":"image","source":{"type":"base64","media_type":%q,"data":"PHN2Zz48L3N2Zz4="}}`, mt)
		block := gjson.Parse(blockJSON)
		userParts := convertImageDocumentBlock("image", block, "user")
		if len(userParts) != 1 {
			t.Fatalf("user role: expected 1 part for %s, got %d", mt, len(userParts))
		}
		if gjson.Get(userParts[0], "inlineData").Exists() {
			t.Fatalf("user role: SVG type %q was packaged as inlineData, Gemini will reject with 400", mt)
		}
		if text := gjson.Get(userParts[0], "text").String(); !strings.Contains(text, "<svg>") {
			t.Fatalf("user role: expected decoded SVG text for %s, got %q", mt, text)
		}

		// Assistant role: also not treated as binary placeholder, but decoded text
		asstParts := convertImageDocumentBlock("image", block, "assistant")
		if len(asstParts) != 1 {
			t.Fatalf("assistant role: expected 1 part for %s, got %d", mt, len(asstParts))
		}
		if gjson.Get(asstParts[0], "inlineData").Exists() {
			t.Fatalf("assistant role: SVG type %q was packaged as inlineData", mt)
		}
		if strings.Contains(gjson.Get(asstParts[0], "text").String(), "[Assistant output") {
			t.Fatalf("assistant role: SVG type %q was treated as binary raster placeholder", mt)
		}
	}
}

func TestConvertImageDocumentBlock_AssistantRoleBinaryImageBecomesPlaceholder(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}`))
	parts := convertImageDocumentBlock("image", block, "assistant")
	if len(parts) != 1 {
		t.Fatalf("expected exactly 1 part, got %d", len(parts))
	}
	text := gjson.Get(parts[0], "text").String()
	if !strings.Contains(text, "[Assistant output image") {
		t.Fatalf("expected a placeholder for a binary image in model role, got %q", text)
	}
}

func TestConvertImageDocumentBlock_MissingMediaTypeDefaultsPerType(t *testing.T) {
	imgBlock := gjson.Parse(mustJSON(t, `{"type":"image","source":{"type":"base64","data":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"}}`))
	parts := convertImageDocumentBlock("image", imgBlock, "user")
	if !gjson.Get(parts[0], "inlineData").Exists() || gjson.Get(parts[0], "inlineData.mimeType").String() != "image/png" {
		t.Fatalf("expected image/png default, got %v", parts)
	}

	docBlock := gjson.Parse(mustJSON(t, `{"type":"document","source":{"type":"base64","data":"JVBERi0xLjQK"}}`))
	parts = convertImageDocumentBlock("document", docBlock, "user")
	var inline gjson.Result
	for _, p := range parts {
		if v := gjson.Get(p, "inlineData"); v.Exists() {
			inline = v
		}
	}
	if inline.Get("mimeType").String() != "application/pdf" {
		t.Fatalf("expected application/pdf default, got %v", parts)
	}
}

func TestConvertImageDocumentBlock_DataURIPrefixStripped(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"}}`))
	parts := convertImageDocumentBlock("image", block, "user")
	if gjson.Get(parts[0], "inlineData.data").String() != "iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB" {
		t.Fatalf("expected the data: URI prefix stripped, got %v", parts)
	}
}

func TestConvertImageDocumentBlock_DocumentTitleAndContextSurfaced(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"document","title":"report.pdf","context":"Q3 numbers","source":{"type":"text","data":"body text"}}`))
	parts := convertImageDocumentBlock("document", block, "user")
	if len(parts) != 2 {
		t.Fatalf("expected a label part plus the body text part, got %d: %v", len(parts), parts)
	}
	label := gjson.Get(parts[0], "text").String()
	if !strings.Contains(label, "report.pdf") || !strings.Contains(label, "Q3 numbers") {
		t.Fatalf("expected title+context in the label, got %q", label)
	}
}

// --- toolcall.go ---

func TestConvertToolUseBlock_RoleInversionDegradesToText(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"tool_use","id":"toolu_1","name":"Read","input":{}}`))
	tn := toolNameInfo{effectiveToolNames: map[string]bool{"Read": true}, disambiguatedToolName: map[string]string{}}
	parts := convertToolUseBlock(block, "user", tn, true)
	if len(parts) != 1 || gjson.Get(parts[0], "functionCall").Exists() {
		t.Fatalf("a tool_use in a user turn must never become a functionCall, got %v", parts)
	}
}

// A tool this request no longer declares is not replayed as a functionCall
// (Gemini would 400) and is not narrated in the model turn either: the model
// reproduced such a "(Note: an earlier call to the X tool ... is omitted ...)"
// text as its own visible output (seen with TaskUpdate / TaskCreate).
func TestConvertToolUseBlock_UndeclaredToolIsDroppedSilently(t *testing.T) {
	for _, name := range []string{"Grep", "TaskCreate", "TaskUpdate"} {
		block := gjson.Parse(mustJSON(t, `{"type":"tool_use","id":"toolu_1","name":"`+name+`","input":{"pattern":"foo"}}`))
		tn := toolNameInfo{effectiveToolNames: map[string]bool{}, disambiguatedToolName: map[string]string{}}
		if parts := convertToolUseBlock(block, "assistant", tn, true); len(parts) != 0 {
			t.Errorf("%s: an undeclared tool call must produce no part, got %v", name, parts)
		}
	}
}

func TestConvert_UndeclaredEarlierCallThatFailedNeverReachesTheModelTurnAsText(t *testing.T) {
	req := `{"model":"claude-opus-4-5","max_tokens":64,` +
		`"tools":[{"name":"Read","description":"r","input_schema":{"type":"object","properties":{}}}],` +
		`"messages":[{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"text","text":"updating"},{"type":"tool_use","id":"tu1","name":"TaskUpdate","input":{"taskId":"1","status":"completed"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":"Updated task 1"}]},` +
		`{"role":"user","content":"next"}]}`
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{}, "main", "main", "s", "i", true)
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, c := range gjson.GetBytes(res.GeminiBody, "contents").Array() {
		if c.Get("role").String() != "model" {
			continue
		}
		seen = true
		if strings.Contains(c.Raw, "omitted here") || strings.Contains(c.Raw, "TaskUpdate") {
			t.Fatalf("the model turn must not mention the dropped call: %s", c.Raw)
		}
		if !strings.Contains(c.Raw, "updating") {
			t.Fatalf("the model's own text is kept: %s", c.Raw)
		}
	}
	if !seen {
		t.Fatalf("no model turn in %s", res.GeminiBody)
	}
}

func TestConvertToolUseBlock_NullInputBecomesEmptyObject(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"tool_use","id":"toolu_1","name":"Bash","input":null}`))
	tn := toolNameInfo{effectiveToolNames: map[string]bool{"Bash": true}, disambiguatedToolName: map[string]string{}}
	parts := convertToolUseBlock(block, "assistant", tn, true)
	if gjson.Get(parts[0], "functionCall.args").Raw != "{}" {
		t.Fatalf("expected args:{} for a null input, got %v", parts)
	}
}

func TestConvertToolUseBlock_UsesDisambiguatedName(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"tool_use","id":"toolu_1","name":"docker:run","input":{}}`))
	tn := toolNameInfo{
		effectiveToolNames:    map[string]bool{"docker:run": true},
		disambiguatedToolName: map[string]string{"docker:run": "docker_run_1"},
	}
	parts := convertToolUseBlock(block, "assistant", tn, true)
	if gjson.Get(parts[0], "functionCall.name").String() != "docker_run_1" {
		t.Fatalf("expected the collision-disambiguated name, got %v", parts)
	}
}

// --- toolresult.go ---

func TestConvertToolResultBlock_RoleInversionDegradesToText(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}`))
	parts := convertToolResultBlock(block, "assistant", map[string]string{"toolu_1": "Read"}, toolNameInfo{effectiveToolNames: map[string]bool{"Read": true}}, gjson.Result{})
	if len(parts) != 1 || gjson.Get(parts[0], "functionResponse").Exists() {
		t.Fatalf("a tool_result in an assistant turn must never become a functionResponse, got %v", parts)
	}
}

func TestConvertToolResultBlock_OrphanResultDegradesToText(t *testing.T) {
	// GAP-V2-06: When toolUseID is not in toolIDToName (tool_use was truncated),
	// emitting a functionResponse causes Gemini HTTP 400. It must degrade to text.
	id := "toolu_Read_1a2b3c_f"
	tn := toolNameInfo{
		declaredToolNames:     map[string]bool{"Read": true},
		effectiveToolNames:    map[string]bool{"Read": true},
		disambiguatedToolName: map[string]string{},
	}
	block := gjson.Parse(mustJSON(t, `{"type":"tool_result","tool_use_id":"`+id+`","content":"file contents"}`))
	parts := convertToolResultBlock(block, "user", map[string]string{}, tn, gjson.Result{})
	if gjson.Get(parts[0], "functionResponse").Exists() {
		t.Fatalf("orphan tool_result without matching tool_use must not become a functionResponse, got %v", parts)
	}
	if !strings.Contains(gjson.Get(parts[0], "text").String(), "[Prior tool result for toolu_Read_1a2b3c_f omitted]") {
		t.Fatalf("expected omitted notice in text, got %v", parts)
	}
}

func TestConvertToolResultBlock_BareStringArrayElementsConcatenated(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"tool_result","tool_use_id":"toolu_1","content":["line one\n","line two\n"]}`))
	tn := toolNameInfo{effectiveToolNames: map[string]bool{"Read": true}, disambiguatedToolName: map[string]string{}}
	parts := convertToolResultBlock(block, "user", map[string]string{"toolu_1": "Read"}, tn, gjson.Result{})
	result := gjson.Get(parts[0], "functionResponse.response.result").String()
	if result != "line one\nline two\n" {
		t.Fatalf("expected both bare-string array elements concatenated, got %q", result)
	}
}

func TestConvertToolResultBlock_IsErrorUsesErrorField(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"tool_result","tool_use_id":"toolu_1","content":"boom","is_error":true}`))
	tn := toolNameInfo{effectiveToolNames: map[string]bool{"Bash": true}, disambiguatedToolName: map[string]string{}}
	parts := convertToolResultBlock(block, "user", map[string]string{"toolu_1": "Bash"}, tn, gjson.Result{})
	if gjson.Get(parts[0], "functionResponse.response.error").String() != "boom" {
		t.Fatalf("expected is_error:true to use the 'error' response field, got %v", parts)
	}
}

func TestConvertToolResultBlock_UndeclaredToolDegradesToText(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"tool_result","tool_use_id":"toolu_1","content":"result text"}`))
	tn := toolNameInfo{effectiveToolNames: map[string]bool{}, disambiguatedToolName: map[string]string{}}
	parts := convertToolResultBlock(block, "user", map[string]string{"toolu_1": "Grep"}, tn, gjson.Result{})
	if gjson.Get(parts[0], "functionResponse").Exists() {
		t.Fatalf("a tool_result for a tool no longer declared must not become a functionResponse")
	}
}

func TestConvertToolResultBlock_JSONResultIsRaw(t *testing.T) {
	// GAP-V2-20: If tool result is valid JSON object/array, it should be serialized raw into response.result
	block := gjson.Parse(mustJSON(t, `{"type":"tool_result","tool_use_id":"toolu_1","content":"{\"status\":\"success\",\"code\":200}"}`))
	tn := toolNameInfo{effectiveToolNames: map[string]bool{"Bash": true}, disambiguatedToolName: map[string]string{}}
	parts := convertToolResultBlock(block, "user", map[string]string{"toolu_1": "Bash"}, tn, gjson.Result{})
	resObj := gjson.Get(parts[0], "functionResponse.response.result")
	if !resObj.IsObject() || resObj.Get("code").Int() != 200 {
		t.Fatalf("expected raw JSON object in functionResponse.response.result, got %v", parts)
	}
}

// --- thinking.go ---

func TestConvertThinkingBlock_OnlyLastAssistantTurnSurvives(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"thinking","thinking":"reasoning...","signature":"sig1"}`))
	content := gjson.Parse(`[]`)
	if parts := convertThinkingBlock(block, "assistant", false, content, true, "sess1", ""); parts != nil {
		t.Fatalf("an older (non-last) assistant turn's thinking must be dropped, got %v", parts)
	}
	parts := convertThinkingBlock(block, "assistant", true, content, true, "sess1", "")
	if len(parts) != 1 || gjson.Get(parts[0], "thought").Bool() != true {
		t.Fatalf("the last assistant turn's thinking must survive as thought:true, got %v", parts)
	}
}

func TestConvertThinkingBlock_PublicAPIKeyModeDegradesToText(t *testing.T) {
	// GAP-THK-01: the public Gemini Developer API (direct API key,
	// isAntigravity=false) rejects any request with a "thought":true part
	// in its history — even on the last assistant turn, where Antigravity
	// mode keeps it as a real thought part.
	block := gjson.Parse(mustJSON(t, `{"type":"thinking","thinking":"reasoning...","signature":"sig1"}`))
	parts := convertThinkingBlock(block, "assistant", true, gjson.Parse(`[]`), false, "sess1", "")
	if len(parts) != 1 || gjson.Get(parts[0], "thought").Exists() {
		t.Fatalf("on the public API, thinking must degrade to plain text even on the last assistant turn, got %v", parts)
	}
	if gjson.Get(parts[0], "text").String() != "reasoning..." {
		t.Fatalf("expected the reasoning text preserved as plain text, got %v", parts)
	}
}

func TestConvertThinkingBlock_RoleInversionDegradesToText(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"thinking","thinking":"reasoning..."}`))
	parts := convertThinkingBlock(block, "user", true, gjson.Parse(`[]`), true, "sess1", "")
	if gjson.Get(parts[0], "thought").Exists() {
		t.Fatalf("a thinking block in a user turn must degrade to plain text")
	}
}

func TestConvertThinkingBlock_RecoversSignatureFromSiblingToolUse(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"thinking","thinking":"reasoning...","signature":"skip_thought_signature_validator"}`))
	content := gjson.Parse(mustJSON(t, `[{"type":"tool_use","id":"toolu_sig_test_1"}]`))
	toolsig.Set("toolu_sig_test_1", "real-signature", "")
	parts := convertThinkingBlock(block, "assistant", true, content, true, "sess1", "")
	if gjson.Get(parts[0], "thoughtSignature").String() != "real-signature" {
		t.Fatalf("expected the sibling tool_use's cached real signature, got %v", parts)
	}
}

func TestConvertThinkingBlock_RecoversSignatureFromSessionFallback(t *testing.T) {
	block := gjson.Parse(mustJSON(t, `{"type":"thinking","thinking":"reasoning...","signature":"skip_thought_signature_validator"}`))
	content := gjson.Parse(mustJSON(t, `[{"type":"tool_use","id":"toolu_no_sig"}]`))
	toolsig.SetSessionSignature("sess_fallback_1", "session-real-sig", "")
	parts := convertThinkingBlock(block, "assistant", true, content, true, "sess_fallback_1", "")
	if len(parts) == 0 || gjson.Get(parts[0], "thoughtSignature").String() != "session-real-sig" {
		t.Fatalf("expected session-level fallback signature, got %v", parts)
	}
}

func TestConvertRedactedThinkingBlock_OlderTurnDropsSignature(t *testing.T) {
	parts := convertRedactedThinkingBlock("assistant", false, true)
	if len(parts) != 0 {
		t.Fatalf("an older redacted_thinking block must be dropped, not narrated, got %v", parts)
	}
	parts = convertRedactedThinkingBlock("assistant", true, true)
	if !gjson.Get(parts[0], "thought").Bool() {
		t.Fatalf("the last assistant turn's redacted_thinking must stay thought:true, got %v", parts)
	}
}

// GAP-TH-02: on the public Gemini Developer API (!isAntigravity), even
// the last assistant turn's redacted_thinking must degrade to plain
// text — thought:true is rejected on ANY part in request history there.
func TestConvertRedactedThinkingBlock_PublicAPIKeyModeDegradesToText(t *testing.T) {
	parts := convertRedactedThinkingBlock("assistant", true, false)
	if len(parts) != 0 {
		t.Fatalf("expected the block to be dropped on the public API key path, got %v", parts)
	}
}

// --- billing.go ---

func TestScrubBillingHeader_StripsBillingHeaderLine(t *testing.T) {
	out := ScrubBillingHeader("keep this\nx-anthropic-billing-header: secret\nkeep this too")
	if strings.Contains(out, "billing-header") {
		t.Fatalf("billing header line must be stripped, got %q", out)
	}
}

func TestScrubBillingHeader_SelfIDClausePreservesTrailingInstructions(t *testing.T) {
	out := ScrubBillingHeader("You are Claude Code, Anthropic's official CLI for Claude. Complete the task fully.")
	if strings.Contains(strings.ToLower(out), "claude code") {
		t.Fatalf("self-ID clause must be replaced, got %q", out)
	}
	if !strings.Contains(out, "Complete the task fully.") {
		t.Fatalf("trailing real instructions on the same line must survive, got %q", out)
	}
}

func TestScrubBillingHeader_EnvironmentSectionKeepsLoadBearingFields(t *testing.T) {
	in := "# Environment\n - Platform: win32\n - Shell: bash\n - noise line\n - noise line 2\n\n# Next"
	out := ScrubBillingHeader(in)
	if !strings.Contains(out, "Platform: win32") {
		t.Fatalf("Platform line is load-bearing and must survive, got %q", out)
	}
	if strings.Contains(out, "noise line") {
		t.Fatalf("unlisted Environment lines must be dropped, got %q", out)
	}
	if !strings.Contains(out, "# Next") {
		t.Fatalf("the next real heading must survive, got %q", out)
	}
}

func TestScrubBillingHeader_CurrentLocalTimeQuantizedToHour(t *testing.T) {
	in := "# Environment\n - current local time: 2026-10-17T11:56:07+03:00"
	out := ScrubBillingHeader(in)
	if !strings.Contains(out, "T11:00:00+03:00") {
		t.Fatalf("expected minutes/seconds zeroed, got %q", out)
	}
}

// --- translate.go (end-to-end) ---

func TestConvertClaudeRequestToGemini_DisableParallelToolUseAddsSystemNudge(t *testing.T) {
	req := mustJSON(t, `{
		"model":"claude-sonnet-4-5",
		"tool_choice":{"type":"auto","disable_parallel_tool_use":true},
		"messages":[{"role":"user","content":"hi"}]
	}`)
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{DefaultModel: "gemini-3.8-flash-tiered"}, "", "main", "sess-nudge", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sysText := gjson.GetBytes(res.GeminiBody, "system_instruction.parts.0.text").String()
	if !strings.Contains(sysText, "at most ONE tool") {
		t.Fatalf("expected the parallel-tool-use nudge in the system instruction, got %q", sysText)
	}
}

func TestConvertClaudeRequestToGemini_LoopGuardNudgeIsolatedFromToolResult(t *testing.T) {
	sessionID := "sess-loop-isolation-test"
	loopguard.RecordPendingNudge(sessionID, loopguard.NudgeReadLoop)

	req := mustJSON(t, `{
		"model":"claude-sonnet-4-5",
		"system":"You are a helpful assistant.",
		"messages":[
			{
				"role":"assistant",
				"content":[{"type":"tool_use","id":"call_1","name":"mcp__tool","input":{}}]
			},
			{
				"role":"user",
				"content":[{"type":"tool_result","tool_use_id":"call_1","content":"{\"status\":\"success\",\"records\":42}"}]
			}
		]
	}`)
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{DefaultModel: "gemini-3.8-flash-tiered"}, "", "main", sessionID, "", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	bodyStr := string(res.GeminiBody)
	// Tool result in conversation contents must NOT contain gateway notices
	contentsJSON := gjson.GetBytes(res.GeminiBody, "contents").String()
	if strings.Contains(contentsJSON, "Notice from the gateway") {
		t.Fatalf("contents contains gateway notice, tool_result was polluted: %s", contentsJSON)
	}
	if !strings.Contains(contentsJSON, "success") || !strings.Contains(contentsJSON, "records") {
		t.Fatalf("contents lost tool_result data: %s", contentsJSON)
	}

	// System instruction parts must contain the gateway notice
	parts := gjson.GetBytes(res.GeminiBody, "system_instruction.parts").Array()
	foundNudge := false
	for _, p := range parts {
		if strings.Contains(p.Get("text").String(), "unproductive read-loop") {
			foundNudge = true
			break
		}
	}
	if !foundNudge {
		t.Fatalf("expected gateway notice in system_instruction.parts, got body: %s", bodyStr)
	}
}

func TestConvertClaudeRequestToGemini_ToolDeclarationsSanitizedAndPresent(t *testing.T) {
	req := mustJSON(t, `{
		"model":"claude-sonnet-4-5",
		"tools":[{"name":"mcp__brave-search__search","description":"search","input_schema":{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}}],
		"messages":[{"role":"user","content":"hi"}]
	}`)
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{DefaultModel: "gemini-3.8-flash-tiered"}, "", "main", "sess-tools", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	name := gjson.GetBytes(res.GeminiBody, "tools.0.functionDeclarations.0.name").String()
	if name != "mcp__brave_search__search" {
		t.Fatalf("expected the sanitized function name, got %q", name)
	}
	if orig := res.ReverseToolNameMap[name]; orig != "mcp__brave-search__search" {
		t.Fatalf("expected ReverseToolNameMap to restore the original name, got %q", orig)
	}
}

func TestConvertClaudeRequestToGemini_EmptyModelFallsBackToTenantDefault(t *testing.T) {
	req := mustJSON(t, `{"model":"","messages":[{"role":"user","content":"hi"}]}`)
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{DefaultModel: "gemini-2.5-flash"}, "", "main", "sess-default", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.ResolvedModel != "gemini-2.5-flash" {
		t.Fatalf("expected the tenant default model, got %q", res.ResolvedModel)
	}
}

func TestConvertClaudeRequestToGemini_MessagesTranslatedToGeminiContents(t *testing.T) {
	req := mustJSON(t, `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hello there"}]}`)
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{DefaultModel: "gemini-3.8-flash-tiered"}, "", "main", "sess-msg", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	role := gjson.GetBytes(res.GeminiBody, "contents.0.role").String()
	text := gjson.GetBytes(res.GeminiBody, "contents.0.parts.0.text").String()
	if role != "user" || text != "hello there" {
		t.Fatalf("expected a translated user turn, got role=%q text=%q", role, text)
	}
}

// GAP-P0-01: forcing tool_choice:"any" sets toolConfig.functionCallingConfig.mode
// to "ANY" — Gemini unconditionally rejects that combined with an active
// thinkingConfig ("Thinking is not supported when function_calling_config.mode
// is ANY"). The gateway must suppress thinking in that case rather than send
// both and let the whole request 400 upstream.
func TestConvertClaudeRequestToGemini_ToolChoiceAnySuppressesThinking(t *testing.T) {
	req := mustJSON(t, `{
		"model":"claude-opus-4-5",
		"tools":[{"name":"Bash","description":"run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],
		"tool_choice":{"type":"any"},
		"messages":[{"role":"user","content":"run ls"}]
	}`)
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{DefaultModel: "gemini-3.8-flash-tiered"}, "", "main", "sess-any-thinking", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode := gjson.GetBytes(res.GeminiBody, "toolConfig.functionCallingConfig.mode").String(); mode != "ANY" {
		t.Fatalf("expected forced mode ANY, got %q", mode)
	}
	if budget := gjson.GetBytes(res.GeminiBody, "generationConfig.thinkingConfig.thinkingBudget"); budget.Exists() && budget.Int() != 0 {
		t.Fatalf("expected thinkingBudget to be suppressed to 0 alongside mode:ANY, got %v", budget.Int())
	}
}

func TestConvertClaudeRequestToGemini_ToolChoiceAutoSetsModeAUTO(t *testing.T) {
	// GAP-TL-08: tool_choice:"auto" sets toolConfig.functionCallingConfig.mode to "AUTO"
	req := mustJSON(t, `{
		"model":"claude-opus-4-5",
		"tools":[{"name":"Bash","description":"run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}],
		"tool_choice":{"type":"auto"},
		"messages":[{"role":"user","content":"run ls"}]
	}`)
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{DefaultModel: "gemini-3.8-flash-tiered"}, "", "main", "sess-auto", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode := gjson.GetBytes(res.GeminiBody, "toolConfig.functionCallingConfig.mode").String(); mode != "AUTO" {
		t.Fatalf("expected mode AUTO, got %q", mode)
	}
}

func TestComputeToolNameInfo_ServerBuiltinToolsRecognized(t *testing.T) {
	req := mustJSON(t, `{
			"model":"claude-opus-4-5",
			"tools":[{"type":"text_editor_20250124"},{"type":"bash_20250124"}],
			"messages":[{"role":"user","content":"hello"}]
		}`)
	tn := computeToolNameInfo(gjson.Parse(req), false)
	if !tn.effectiveToolNames["str_replace_editor"] {
		t.Fatalf("expected str_replace_editor to be in effectiveToolNames, got: %v", tn.effectiveToolNames)
	}
	if !tn.effectiveToolNames["bash"] {
		t.Fatalf("expected bash to be in effectiveToolNames, got: %v", tn.effectiveToolNames)
	}
}

func TestTools_WebSearch_NeverDeclaredAsCustomFunction(t *testing.T) {
	// web_search is an Anthropic server-side tool, not a client-executable tool.
	// Declaring it in functionDeclarations causes Gemini to call it, which crashes
	// Claude Code CLI with "Error: No such tool available: web_search".
	// It must never be declared as a function declaration.
	req := mustJSON(t, `{
			"model":"claude-opus-4-5",
			"tools":[
				{"name":"Bash","description":"run a command","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}},
				{"name":"web_search","description":"search web","input_schema":{"type":"object","properties":{"query":{"type":"string"}}}}
			],
			"messages":[{"role":"user","content":"search something"}]
		}`)
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{DefaultModel: "gemini-3.8-flash-tiered"}, "", "main", "sess-ws-coexist", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	decls := gjson.GetBytes(res.GeminiBody, "tools.0.functionDeclarations").Array()
	for _, d := range decls {
		if d.Get("name").String() == "web_search" {
			t.Fatalf("web_search must never be declared in functionDeclarations")
		}
	}
}

func TestScrubBillingHeader_NoDanglingPossessive(t *testing.T) {
	input := "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	scrubbed := ScrubBillingHeader(input)
	if strings.Contains(scrubbed, "'s") {
		t.Fatalf("expected no dangling possessive, got: %q", scrubbed)
	}
	if !strings.Contains(scrubbed, "a coding assistant") {
		t.Fatalf("expected replacement with 'a coding assistant', got: %q", scrubbed)
	}
}

func TestConvertImageDocumentBlock_AssistantURLDoc_ReplacedWithPlaceholder(t *testing.T) {
	docBlock := gjson.Parse(`{"type":"document","source":{"type":"url","url":"https://example.com/spec.pdf"}}`)
	parts := convertImageDocumentBlock("document", docBlock, "assistant")
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if gjson.Parse(parts[0]).Get("inlineData").Exists() {
		t.Fatalf("assistant role must never emit inlineData, got: %s", parts[0])
	}
	txt := gjson.Parse(parts[0]).Get("text").String()
	if txt != "[Assistant output document: url]" {
		t.Fatalf("expected exact placeholder [Assistant output document: url], got: %q", txt)
	}
}

func TestDedupRepeatedReadsBeforeTruncation(t *testing.T) {
	// Verify that repeated reads are deduplicated before context window truncation
	largeContent := strings.Repeat("A", 200000)
	req := fmt.Sprintf(`{
			"model":"claude-opus-4-5",
			"tools":[{"name":"Read","description":"read file","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}],
			"messages":[
				{"role":"user","content":"read file first time"},
				{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{"file_path":"H:\\file.txt"}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":%q}]},
				{"role":"assistant","content":[{"type":"tool_use","id":"call_2","name":"Read","input":{"file_path":"H:\\file.txt"}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_2","content":%q}]},
				{"role":"assistant","content":"done"}
			]
		}`, largeContent, largeContent)

	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{DefaultModel: "gemini-2.5-flash"}, "", "main", "sess-dedup-trunc", "", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Since repeated read was deduplicated, it was shrunk and didn't blow past context window
	if !res.WasShrunk {
		t.Fatalf("expected WasShrunk to be true due to repeated read deduplication")
	}
}

func TestConvertServerToolUseBlock_DoesNotBecomeFunctionCall(t *testing.T) {
	block := gjson.Parse(`{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{"query":"golang"}}`)
	parts := convertServerToolUseBlock(block)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if gjson.Get(parts[0], "functionCall").Exists() {
		t.Fatalf("server_tool_use must never produce a functionCall part, got %s", parts[0])
	}
	if !strings.Contains(gjson.Get(parts[0], "text").String(), "web_search") {
		t.Fatalf("expected text to mention web_search, got %s", parts[0])
	}
}

func TestComputeToolNameInfo_BareStringToolChoiceAny(t *testing.T) {
	req := mustJSON(t, `{
		"tools":[{"name":"Grep","description":"search"},{"name":"Glob","description":"glob"}],
		"tool_choice":"any"
	}`)
	tn := computeToolNameInfo(gjson.Parse(req), true)
	if !tn.keepSearchToolsForForcedAny {
		t.Fatalf("expected keepSearchToolsForForcedAny to be true for bare-string tool_choice:any")
	}
}

func TestConvertToolUseBlock_ThoughtSignatureOmittedWhenNotAntigravity(t *testing.T) {
	block := gjson.Parse(`{"type":"tool_use","id":"call_123","name":"Bash","input":{"command":"ls"}}`)
	tn := toolNameInfo{effectiveToolNames: map[string]bool{"Bash": true}}
	parts := convertToolUseBlock(block, "assistant", tn, false)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if gjson.Get(parts[0], "thoughtSignature").Exists() {
		t.Fatalf("public API key (!isAntigravity) must not attach thoughtSignature, got %s", parts[0])
	}
}

func TestApplyGenerationConfig_ZeroMaxTokens_DeletesThinkingConfig(t *testing.T) {
	root := gjson.Parse(`{"max_tokens": 0, "thinking": {"type": "enabled", "budget_tokens": 2048}}`)
	out, budget := applyGenerationConfig("{}", root, "gemini-2.5-pro", 2048, true)
	if budget != 0 {
		t.Fatalf("expected budget=0 for max_tokens:0, got %d", budget)
	}
	if gjson.Get(out, "generationConfig.thinkingConfig").Exists() {
		t.Fatalf("thinkingConfig must be completely deleted when max_tokens:0, got %s", out)
	}
	if gjson.Get(out, "generationConfig.maxOutputTokens").Int() != 1 {
		t.Fatalf("expected maxOutputTokens=1, got %d", gjson.Get(out, "generationConfig.maxOutputTokens").Int())
	}
}

// A model turn that held nothing but a dropped (undeclared) call is not left
// empty: it gets the placeholder part, signed for Antigravity, which rejects a
// model turn without a thought signature.
func TestConvert_ModelTurnOfOnlyADroppedCallKeepsASignedPlaceholder(t *testing.T) {
	req := `{"model":"claude-opus-4-5","max_tokens":64,` +
		`"tools":[{"name":"Read","description":"r","input_schema":{"type":"object","properties":{}}}],` +
		`"messages":[{"role":"user","content":"go"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"TaskUpdate","input":{"taskId":"1"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":"ok"}]}]}`
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{}, "main", "main", "s", "i", true)
	if err != nil {
		t.Fatal(err)
	}
	var model gjson.Result
	for _, c := range gjson.GetBytes(res.GeminiBody, "contents").Array() {
		if c.Get("role").String() == "model" {
			model = c
		}
	}
	if !model.Exists() || model.Get("parts.0.text").String() == "" || model.Get("parts.0.thoughtSignature").String() != "skip_thought_signature_validator" {
		t.Fatalf("expected a signed placeholder, got %s", model.Raw)
	}
	if strings.Contains(model.Raw, "TaskUpdate") || strings.Contains(model.Raw, "omitted") {
		t.Fatalf("the dropped call must not be narrated: %s", model.Raw)
	}
}

func TestConvertWebSearchToolResultBlock_MarkdownFormatting(t *testing.T) {
	block := gjson.Parse(`{
		"type": "web_search_tool_result",
		"tool_use_id": "srv_123",
		"content": [
			{
				"type": "web_search_result",
				"title": "Example Domain",
				"url": "https://example.com",
				"snippet": "This domain is for use in illustrative examples in documents."
			}
		]
	}`)
	parts := convertWebSearchToolResultBlock(block)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	text := gjson.Get(parts[0], "text").String()
	if !strings.Contains(text, "[Web Search Results]:") {
		t.Fatalf("expected header in web search result, got %s", text)
	}
	if !strings.Contains(text, "Example Domain") || !strings.Contains(text, "https://example.com") {
		t.Fatalf("expected title and url in web search result, got %s", text)
	}
}

func TestTranslateMessages_MultimodalOrder_ImageAndText(t *testing.T) {
	root := gjson.Parse(`{
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "image",
						"source": {
							"type": "base64",
							"media_type": "image/png",
							"data": "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
						}
					},
					{
						"type": "text",
						"text": "What is in this image?"
					}
				]
			}
		]
	}`)
	out := translateMessages("{}", root, toolNameInfo{}, true, "session1", "")
	parts := gjson.Get(out, "contents.0.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("expected 2 parts, got %d (full: %s)", len(parts), out)
	}
	if !gjson.Get(parts[0].Raw, "inlineData").Exists() {
		t.Fatalf("expected first part to be inlineData, got %s", parts[0].Raw)
	}
	if gjson.Get(parts[1].Raw, "text").String() != "What is in this image?" {
		t.Fatalf("expected second part to be text, got %s", parts[1].Raw)
	}
}

func TestApplyGenerationConfig_ThinkingDisabled_PreservesZeroTemperature(t *testing.T) {
	req := gjson.Parse(`{
		"temperature": 0.0,
		"max_tokens": 4096,
		"thinking": {"type": "disabled"}
		}`)
	out, budget := applyGenerationConfig("{}", req, "gemini-3.8-flash-tiered", 0, true)
	if budget != 0 {
		t.Fatalf("expected budget 0, got %d", budget)
	}
	if gjson.Get(out, "generationConfig.thinkingConfig.includeThoughts").Bool() != false {
		t.Fatalf("expected includeThoughts=false, got %s", out)
	}
	if gjson.Get(out, "generationConfig.thinkingConfig.thinkingBudget").Int() != 0 {
		t.Fatalf("expected thinkingBudget=0, got %s", out)
	}
	if gjson.Get(out, "generationConfig.temperature").Float() != 0.0 {
		t.Fatalf("expected temperature 0.0 preserved when thinking disabled, got %f", gjson.Get(out, "generationConfig.temperature").Float())
	}
	if gjson.Get(out, "generationConfig.maxOutputTokens").Int() != 4096 {
		t.Fatalf("expected maxOutputTokens 4096, got %d", gjson.Get(out, "generationConfig.maxOutputTokens").Int())
	}
}

func TestApplyGenerationConfig_ModeAuto_InflatesMaxOutputTokens(t *testing.T) {
	req := gjson.Parse(`{
		"temperature": 1.0,
		"max_tokens": 8192
		}`)
	out, budget := applyGenerationConfig("{}", req, "gemini-3.8-flash-tiered", -1, true)
	if budget != -1 {
		t.Fatalf("expected budget -1, got %d", budget)
	}
	if gjson.Get(out, "generationConfig.thinkingConfig.thinkingBudget").Int() != -1 {
		t.Fatalf("expected thinkingBudget=-1 for Antigravity ModeAuto, got %d", gjson.Get(out, "generationConfig.thinkingConfig.thinkingBudget").Int())
	}
	if gjson.Get(out, "generationConfig.thinkingConfig.includeThoughts").Bool() != true {
		t.Fatalf("expected includeThoughts=true, got %s", out)
	}
	if gjson.Get(out, "generationConfig.maxOutputTokens").Int() != 64000 {
		t.Fatalf("expected maxOutputTokens inflated to 64000 for ModeAuto, got %d", gjson.Get(out, "generationConfig.maxOutputTokens").Int())
	}
}

func TestApplyGenerationConfig_ExtendedThinking_AdjustsAtCeiling(t *testing.T) {
	req := gjson.Parse(`{
		"temperature": 0.2,
		"max_tokens": 4096
		}`)
	// budget == 64000 (at ceiling)
	out, adjBudget := applyGenerationConfig("{}", req, "gemini-3.8-flash-tiered", 64000, true)
	if adjBudget != 64000-1024 {
		t.Fatalf("expected budget adjusted to 62976 (ceiling - 1024), got %d", adjBudget)
	}
	if gjson.Get(out, "generationConfig.thinkingConfig.thinkingBudget").Int() != 62976 {
		t.Fatalf("expected thinkingBudget in JSON to be 62976, got %d", gjson.Get(out, "generationConfig.thinkingConfig.thinkingBudget").Int())
	}
	if gjson.Get(out, "generationConfig.maxOutputTokens").Int() != 64000 {
		t.Fatalf("expected maxOutputTokens to be 64000, got %d", gjson.Get(out, "generationConfig.maxOutputTokens").Int())
	}
	// Thinking active forces temperature to 1.0
	if gjson.Get(out, "generationConfig.temperature").Float() != 1.0 {
		t.Fatalf("expected temperature forced to 1.0 when thinking active, got %f", gjson.Get(out, "generationConfig.temperature").Float())
	}
}

func TestAdversarial_SVG_ConvertsToTextPart(t *testing.T) {
	svgXML := `<svg width="50" height="50"><rect width="50" height="50" style="fill:blue;" /></svg>`
	b64SVG := base64.StdEncoding.EncodeToString([]byte(svgXML))
	block := gjson.Parse(`{"type":"image","source":{"type":"base64","media_type":"image/svg+xml","data":"` + b64SVG + `"}}`)

	parts := convertImageDocumentBlock("image", block, "user")
	if len(parts) != 1 {
		t.Fatalf("expected 1 part for SVG, got %d", len(parts))
	}
	// Verify it is a text part containing SVG XML, NOT inlineData
	if gjson.Get(parts[0], "inlineData").Exists() {
		t.Errorf("SVG must NOT be converted to inlineData, got %s", parts[0])
	}
	gotText := gjson.Get(parts[0], "text").String()
	if gotText != svgXML {
		t.Errorf("SVG text = %q, want %q", gotText, svgXML)
	}
}

func TestAdversarial_Document_SourceContent(t *testing.T) {
	// 1. Literal string content
	blockStr := gjson.Parse(`{
			"type": "document",
			"title": "notes.txt",
			"context": "meeting notes",
			"source": {
				"type": "content",
				"content": "Line 1\nLine 2"
			}
		}`)
	partsStr := convertImageDocumentBlock("document", blockStr, "user")
	if len(partsStr) != 2 {
		t.Fatalf("expected 2 parts (context label + content), got %d: %v", len(partsStr), partsStr)
	}
	if !strings.Contains(partsStr[0], `notes.txt`) || !strings.Contains(partsStr[0], `meeting notes`) {
		t.Errorf("part 0 must have document label, got %s", partsStr[0])
	}
	if gjson.Get(partsStr[1], "text").String() != "Line 1\nLine 2" {
		t.Errorf("part 1 text = %q, want Line 1\\nLine 2", gjson.Get(partsStr[1], "text").String())
	}

	// 2. Array of text blocks content
	blockArr := gjson.Parse(`{
			"type": "document",
			"source": {
				"type": "content",
				"content": [
					{"type": "text", "text": "Part A "},
					{"type": "text", "text": "Part B"}
				]
			}
		}`)
	partsArr := convertImageDocumentBlock("document", blockArr, "user")
	if len(partsArr) != 1 {
		t.Fatalf("expected 1 part, got %d", len(partsArr))
	}
	if gjson.Get(partsArr[0], "text").String() != "Part A Part B" {
		t.Errorf("combined text = %q, want Part A Part B", gjson.Get(partsArr[0], "text").String())
	}
}

func TestAdversarial_Document_SourceURL_And_File(t *testing.T) {
	// Assistant role URL attachment must become a placeholder
	blockURL := gjson.Parse(`{
			"type": "document",
			"source": {
				"type": "url",
				"url": "https://example.com/spec.pdf"
			}
		}`)
	partsAsst := convertImageDocumentBlock("document", blockURL, "assistant")
	if len(partsAsst) != 1 || !strings.Contains(partsAsst[0], "[Assistant output document: url]") {
		t.Errorf("assistant document URL part = %v, want placeholder", partsAsst)
	}

	// Anthropic Files API reference must become a visible placeholder
	blockFile := gjson.Parse(`{
			"type": "document",
			"source": {
				"type": "file",
				"file_id": "file_xyz123"
			}
		}`)
	partsFile := convertImageDocumentBlock("document", blockFile, "user")
	if len(partsFile) != 1 || !strings.Contains(partsFile[0], "file_xyz123") {
		t.Errorf("file part = %v, want Anthropic Files API placeholder", partsFile)
	}
}

func TestAdversarial_HugeToolCallArguments(t *testing.T) {
	// 1.5MB JSON argument inside tool_use
	hugeStr := strings.Repeat("x", 1500000)
	hugeInput := `{"file_path":"/tmp/huge.txt","content":"` + hugeStr + `"}`
	block := gjson.Parse(`{
			"type": "tool_use",
			"id": "toolu_huge_1",
			"name": "Write",
			"input": ` + hugeInput + `
		}`)

	tn := toolNameInfo{
		effectiveToolNames:    map[string]bool{"Write": true},
		disambiguatedToolName: map[string]string{"Write": "Write"},
	}

	parts := convertToolUseBlock(block, "assistant", tn, false)
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	callName := gjson.Get(parts[0], "functionCall.name").String()
	if callName != "Write" {
		t.Errorf("call name = %q, want Write", callName)
	}
	contentLen := len(gjson.Get(parts[0], "functionCall.args.content").String())
	if contentLen != 1500000 {
		t.Errorf("huge content len = %d, want 1500000", contentLen)
	}
}

// --- context overflow / attachments (W3) ---

func overflowBody(t *testing.T, nMsgs int, textLen int) []byte {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[`)
	for i := 0; i < nMsgs; i++ {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"role":%q,"content":[{"type":"text","text":%q}]}`, role, strings.Repeat("lorem ipsum dolor sit amet ", textLen/27))
	}
	sb.WriteString(`,{"role":"user","content":"final question"}]}`)
	return []byte(sb.String())
}

func TestConvert_OverflowIsErrorByDefault_TruncationOptIn(t *testing.T) {
	SetAllowHistoryTruncation(false)
	body := overflowBody(t, 40, 400_000) // ~16MB of text -> far over the window
	_, err := ConvertClaudeRequestToGemini(body, config.TenantConfig{DefaultModel: "gemini-3.8-flash"}, "", "main", "s1", "", true)
	var of *ContextOverflowError
	if !errors.As(err, &of) {
		t.Fatalf("want ContextOverflowError, got %v", err)
	}
	if of.Estimated <= of.Limit || !strings.HasPrefix(of.Error(), "prompt is too long: ") || !strings.HasSuffix(of.Error(), " maximum") {
		t.Fatalf("bad overflow error: %v", of)
	}

	SetAllowHistoryTruncation(true)
	defer SetAllowHistoryTruncation(false)
	res, err := ConvertClaudeRequestToGemini(body, config.TenantConfig{DefaultModel: "gemini-3.8-flash"}, "", "main", "s2", "", true)
	if err != nil {
		// opt-in truncation may still fail if even the protected tail is over budget
		if !errors.As(err, &of) {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	if !res.WasShrunk {
		t.Fatal("opt-in truncation must mark WasShrunk")
	}
}

func TestConvert_NestedScreenshotsPrunedUnderInlineLimit(t *testing.T) {
	SetAllowHistoryTruncation(false)
	img := fmt.Sprintf(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}`, bigPNGB64(200_000))
	var msgs []string
	for i := 0; i < 12; i++ {
		items := strings.TrimSuffix(strings.Repeat(img+",", 10), ",")
		msgs = append(msgs,
			fmt.Sprintf(`{"role":"assistant","content":[{"type":"tool_use","id":"toolu_%d","name":"Shot","input":{}}]}`, i),
			fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_%d","content":[%s]}]}`, i, items))
	}
	body := `{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"go"},` + strings.Join(msgs, ",") + `,{"role":"assistant","content":"ok"},{"role":"user","content":"next"}]}`
	res, err := ConvertClaudeRequestToGemini([]byte(body), config.TenantConfig{DefaultModel: "gemini-3.8-flash"}, "", "main", "s3", "", true)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	total := 0
	gjson.GetBytes(res.GeminiBody, "contents").ForEach(func(_, c gjson.Result) bool {
		c.Get("parts").ForEach(func(_, p gjson.Result) bool {
			total += len(p.Get("inlineData.data").String())
			return true
		})
		return true
	})
	if total > 20<<20 {
		t.Fatalf("inline data still %d bytes after pruning", total)
	}
	if !res.WasShrunk {
		t.Fatal("pruning must be reported as shrink and persisted even with no message drop")
	}
}

func TestSanitizeGeminiFunctionName_LongNameKeepsServerAndToolAndStaysUnique(t *testing.T) {
	a := "mcp__a_very_long_server_name_for_testing_purposes__list_all_the_open_pull_requests"
	b := "mcp__a_very_long_server_name_for_testing_purposes__list_all_the_open_pull_issues__"
	ga, gb := sanitizeGeminiFunctionName(a), sanitizeGeminiFunctionName(b)
	for _, g := range []string{ga, gb} {
		if len(g) != 63 || !strings.HasPrefix(g, "mcp__a_very_long_server_") {
			t.Fatalf("expected 63 chars starting with the server: %q (%d)", g, len(g))
		}
	}
	if ga == gb {
		t.Fatalf("two different tools collapsed into %q", ga)
	}
	if !strings.HasSuffix(ga, "open_pull_requests") || !strings.HasSuffix(sanitizeGeminiFunctionName(b), "pull_issues__") {
		t.Fatalf("the tool name (the end) must survive: %q / %q", ga, gb)
	}
	if sanitizeGeminiFunctionName(a) != ga {
		t.Fatal("not deterministic")
	}
	short := "mcp__brave-search__search"
	if sanitizeGeminiFunctionName(short) != "mcp__brave_search__search" {
		t.Fatal("a short name must be left alone")
	}
}

func TestDeferredToolPlaceholderIsNotDeclared(t *testing.T) {
	req := `{"model":"claude-opus-4-5","max_tokens":64,"tools":[` +
		`{"name":"Read","description":"r","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}},` +
		`{"name":"DeferredToolPlaceholder","description":"p","defer_loading":true,"input_schema":{"type":"object","properties":{}}}],` +
		`"messages":[{"role":"user","content":"hi"}]}`
	res, err := ConvertClaudeRequestToGemini([]byte(req), config.TenantConfig{}, "main", "main", "s", "i", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range gjson.GetBytes(res.GeminiBody, "tools.0.functionDeclarations").Array() {
		if d.Get("name").String() == "DeferredToolPlaceholder" {
			t.Fatalf("the placeholder was declared: %s", d.Raw)
		}
	}
}
