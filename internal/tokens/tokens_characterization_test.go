// Characterization tests for internal/tokens against v1's
// token_estimator.go / context_window.go formulas.
package tokens

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"claudgravity/internal/protocol/claude"
)

func mustReq(t *testing.T, raw string) claude.Request {
	t.Helper()
	var req claude.Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return req
}

func TestWeightedCharTokens_ShortString(t *testing.T) {
	// length<5 branch: ceil(length/3)
	if got := WeightedCharTokens("ab"); got != 1 {
		t.Fatalf("got %d", got)
	}
}

func TestWeightedCharTokens_ASCIIProse(t *testing.T) {
	// 9 regular ASCII chars -> ceil(9/4.5) = 2
	if got := WeightedCharTokens("hello sir"); got != 2 {
		t.Fatalf("got %d", got)
	}
}

func TestFlatTextTokens_MatchesJSONMarshalThenHalve(t *testing.T) {
	s := "hello"
	b, _ := json.Marshal(s)
	want := len(b) / 2
	if want < 1 {
		want = 1
	}
	if got := FlatTextTokens(s); got != want {
		t.Fatalf("got %d want %d", got, want)
	}
}

func TestDocumentCost_PDFBase64_UsesFloorWhenSmall(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("tiny pdf without a readable page count"))
	src := claude.DocumentSource{Type: "base64", MediaType: "application/pdf", Data: data}
	if got := documentCost(src, WeightedCharTokens); got != minPDFTokenFloor {
		t.Fatalf("got %d want %d", got, minPDFTokenFloor)
	}
}

func TestDocumentCost_PDFPageCount_SkipsFloorWhenTrusted(t *testing.T) {
	pdf := "/Type /Pages /Count 1\n"
	data := base64.StdEncoding.EncodeToString([]byte(pdf))
	src := claude.DocumentSource{Type: "base64", MediaType: "application/pdf", Data: data}
	got := documentCost(src, WeightedCharTokens)
	if got != geminiPDFTokensPerPage {
		t.Fatalf("a trustworthy 1-page PDF must price at the real per-page rate (%d), got %d", geminiPDFTokensPerPage, got)
	}
}

func TestDocumentCost_PDFPageCount_TakesLargestOfMultiplePagesNodes(t *testing.T) {
	// A nested/intermediate /Pages node carries its own smaller subtree
	// /Count — the root's total (larger) must win.
	pdf := "/Type /Pages /Count 3 ... /Type /Pages /Count 50"
	data := base64.StdEncoding.EncodeToString([]byte(pdf))
	src := claude.DocumentSource{Type: "base64", MediaType: "application/pdf", Data: data}
	want := 50 * geminiPDFTokensPerPage
	if got := documentCost(src, WeightedCharTokens); got != want {
		t.Fatalf("got %d want %d", got, want)
	}
}

func TestDocumentCost_URLSource_PricesPlaceholderWithGivenTextCost(t *testing.T) {
	// The placeholder-text branches are the one place v1's two estimators
	// genuinely disagree (weighted for the real estimate, flat for the
	// conservative one) — documentCost/imageCost must honor whichever
	// textCost the caller passes, not hardcode one.
	src := claude.DocumentSource{Type: "url", URL: "https://example.com/doc.pdf"}
	wantWeighted := WeightedCharTokens("[Attached by URL: https://example.com/doc.pdf]")
	wantFlat := FlatTextTokens("[Attached by URL: https://example.com/doc.pdf]")
	if got := documentCost(src, WeightedCharTokens); got != wantWeighted {
		t.Fatalf("got %d want %d (weighted)", got, wantWeighted)
	}
	if got := documentCost(src, FlatTextTokens); got != wantFlat {
		t.Fatalf("got %d want %d (flat)", got, wantFlat)
	}
}

func TestDocumentCost_ContentSource_PricesRealExtractedText(t *testing.T) {
	contentJSON := `[{"type":"text","text":"hello world"}]`
	src := claude.DocumentSource{Type: "content", Content: json.RawMessage(contentJSON)}
	got := documentCost(src, WeightedCharTokens)
	want := WeightedCharTokens("hello world")
	if got != want {
		t.Fatalf("got %d want %d", got, want)
	}
}

func TestImageCost_URLSource_PricesPlaceholder(t *testing.T) {
	src := claude.ImageSource{Type: "url", URL: "https://example.com/x.png"}
	got := imageCost(src, WeightedCharTokens)
	want := WeightedCharTokens("[Attached by URL: https://example.com/x.png]")
	if got != want {
		t.Fatalf("got %d want %d", got, want)
	}
}

func TestImageCost_SVG_PricedAsTextNotRaster(t *testing.T) {
	svg := strings.Repeat("<svg>path data here</svg>", 50)
	src := claude.ImageSource{Type: "base64", MediaType: "image/svg+xml", Data: svg}
	got := imageCost(src, WeightedCharTokens)
	want := len(svg) * 3 / 4 / 2
	if got != want {
		t.Fatalf("got %d want %d", got, want)
	}
}

func TestBlockCost_ThinkingPricedOnlyForLastAssistant(t *testing.T) {
	b := claude.NewBlock(claude.ThinkingBlock{Type: "thinking", Thinking: "deep thoughts here"})
	if got := blockCost(b, false, WeightedCharTokens); got != 0 {
		t.Fatalf("non-last-assistant thinking must price at 0, got %d", got)
	}
	if got := blockCost(b, true, WeightedCharTokens); got == 0 {
		t.Fatal("last-assistant thinking must price at its real size")
	}
}

func TestBlockCost_RedactedThinking_PricesFixedPlaceholderAlways(t *testing.T) {
	huge := strings.Repeat("x", 50000)
	b := claude.NewBlock(claude.RedactedThinkingBlock{Type: "redacted_thinking", Data: huge})
	want := WeightedCharTokens("[Prior reasoning redacted]")
	if got := blockCost(b, false, WeightedCharTokens); got != want {
		t.Fatalf("got %d want %d — redacted_thinking must never price the real encrypted blob", got, want)
	}
}

func TestBlockCost_UnknownType_NeverPricesZero(t *testing.T) {
	// This is this merge's deliberate fix: v1's two estimators disagreed
	// on an unrecognized block type (0 in one, flat estimate in the
	// other) — this package always applies the flat estimate.
	raw := []byte(`{"type":"server_tool_use","id":"x","name":"web_search","input":{"query":"something long enough to matter"}}`)
	var b claude.Block
	_ = json.Unmarshal(raw, &b)
	if got := blockCost(b, false, WeightedCharTokens); got == 0 {
		t.Fatal("a recognized-but-not-specially-handled block type must never price at 0")
	}
}

func TestBlockCost_ToolResult_RecursesIntoNestedImageBlock(t *testing.T) {
	raw := []byte(`{"type":"tool_result","tool_use_id":"t1","content":[{"type":"image","source":{"type":"url","url":"https://example.com/shot.png"}}]}`)
	var b claude.Block
	_ = json.Unmarshal(raw, &b)
	got := blockCost(b, false, WeightedCharTokens)
	want := WeightedCharTokens("[Attached by URL: https://example.com/shot.png]")
	if got != want {
		t.Fatalf("got %d want %d", got, want)
	}
}

func TestBlockCost_ToolResult_BareStringArrayElement(t *testing.T) {
	raw := []byte(`{"type":"tool_result","tool_use_id":"t1","content":["line one\n","line two\n"]}`)
	var b claude.Block
	_ = json.Unmarshal(raw, &b)
	got := blockCost(b, false, WeightedCharTokens)
	want := WeightedCharTokens("line one\n") + WeightedCharTokens("line two\n")
	if got != want {
		t.Fatalf("got %d want %d — a bare-string tool_result array element must not price at 0", got, want)
	}
}

func TestWeightedRequestTokens_BuiltinToolWithNoSchema_PricesReferenceSchema(t *testing.T) {
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"name": "bash", "type": "bash_20250124"}]
	}`)
	withBuiltin := WeightedRequestTokens(req)

	req2 := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"name": "custom_tool"}]
	}`)
	withoutSchema := WeightedRequestTokens(req2)

	if withBuiltin <= withoutSchema {
		t.Fatalf("a builtin tool's reference schema must be priced, got withBuiltin=%d withoutSchema=%d", withBuiltin, withoutSchema)
	}
}

func TestTruncateConversationToFit_DropsOldestIntermediateMessages(t *testing.T) {
	big := strings.Repeat("x ", 5000)
	msgsJSON := `[{"role":"user","content":"original task"}`
	for i := 0; i < 10; i++ {
		msgsJSON += `,{"role":"assistant","content":"` + big + `"},{"role":"user","content":"` + big + `"}`
	}
	msgsJSON += `]`
	req := mustReq(t, `{"model":"m","max_tokens":10,"messages":`+msgsJSON+`}`)

	newReq, dropped, truncated := TruncateConversationToFit(req, 2000, 100)
	if !truncated || dropped == 0 {
		t.Fatalf("expected truncation to fire, got dropped=%d truncated=%v", dropped, truncated)
	}
	first := newReq.Messages[0]
	ft, ok := first.Content[0].AsText()
	if !ok || ft.Text != "original task" {
		t.Fatalf("index 0 (the user's original task) must survive untouched, got %+v", first)
	}
	if len(first.Content) < 2 {
		t.Fatalf("expected gateway notice appended to index 0 content blocks, got %d blocks", len(first.Content))
	}
	nt, _ := first.Content[1].AsText()
	if !strings.Contains(nt.Text, "Gateway notice") {
		t.Fatalf("expected a gateway notice in first message content, got %q", nt.Text)
	}
	if newReq.Messages[1].Role != claude.RoleAssistant {
		t.Fatalf("expected index 1 to be assistant role to maintain alternation, got %s", newReq.Messages[1].Role)
	}
}

func TestTruncateConversationToFit_NoticeTextIsIdempotent(t *testing.T) {
	big := strings.Repeat("x ", 500)
	var msgs []claude.Message
	msgs = append(msgs, claude.Message{
		Role: claude.RoleUser,
		Content: []claude.Block{
			claude.NewBlock(claude.TextBlock{Type: "text", Text: "original task"}),
			claude.NewBlock(claude.TextBlock{Type: "text", Text: "\n\n[Gateway notice: Earlier intermediate messages were dropped to fit inside this conversation's context window. The conversation continues below.]"}),
		},
	})
	for i := 0; i < 6; i++ {
		msgs = append(msgs, claude.Message{Role: claude.RoleAssistant, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: big})}})
		msgs = append(msgs, claude.Message{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: big})}})
	}
	req := claude.Request{Model: "m", Messages: msgs}
	newReq, dropped, truncated := TruncateConversationToFit(req, 1000, 100)
	if !truncated || dropped == 0 {
		t.Fatalf("expected truncation, got dropped=%d truncated=%v", dropped, truncated)
	}
	noticeCount := 0
	for _, b := range newReq.Messages[0].Content {
		if tb, ok := b.AsText(); ok && strings.Contains(tb.Text, "Earlier intermediate messages were dropped") {
			noticeCount++
		}
	}
	if noticeCount != 1 {
		t.Fatalf("expected exactly 1 notice block, got %d", noticeCount)
	}
}

// GAP-D4-01 regression: a PDF with a document outline (bookmarks) declares
// its own "/Type /Outlines /Count N" dict, with N being the outline's own
// (usually tiny) entry count, commonly appearing in the raw bytes BEFORE
// the real page tree's "/Type /Pages /Count N". An unanchored /Count scan
// would match the outline's count first and silently 100x-undercount a
// real multi-page PDF, letting an oversized request slip past
// TruncateConversationToFit only to be rejected by the real upstream for
// genuinely exceeding its context window.
func TestEstimatePDFTokensFromPageCount_IgnoresOutlineCountBeforeRealPages(t *testing.T) {
	raw := []byte("%PDF-1.4\n<< /Type /Outlines /Count 1 >>\n<< /Type /Pages /Count 100 >>")
	b64 := base64.StdEncoding.EncodeToString(raw)
	got, ok := estimatePDFTokensFromPageCount(b64)
	if !ok {
		t.Fatalf("expected a page count to be found")
	}
	want := 100 * geminiPDFTokensPerPage
	if got != want {
		t.Fatalf("got %d tokens (outline-count 1*%d=%d would be the regression), want %d", got, geminiPDFTokensPerPage, geminiPDFTokensPerPage, want)
	}
}

// GAP-D4-01, second half: nested/intermediate /Pages subtree nodes carry
// their own smaller subtree /Count — only the MAX across all real /Pages
// matches is the root's true total, never the first one found.
func TestEstimatePDFTokensFromPageCount_TakesMaxAcrossNestedPagesNodes(t *testing.T) {
	raw := []byte("%PDF-1.4\n<< /Type /Pages /Count 3 >>\n<< /Type /Pages /Count 50 >>")
	b64 := base64.StdEncoding.EncodeToString(raw)
	got, ok := estimatePDFTokensFromPageCount(b64)
	if !ok {
		t.Fatalf("expected a page count to be found")
	}
	if want := 50 * geminiPDFTokensPerPage; got != want {
		t.Fatalf("got %d, want max-based %d", got, want)
	}
}

func TestTruncateConversationToFit_NoOpWhenUnderBudget(t *testing.T) {
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "hello"},
			{"role": "user", "content": "how are you"},
			{"role": "assistant", "content": "good"}
		]
	}`)
	_, dropped, truncated := TruncateConversationToFit(req, 1048576, 4096)
	if truncated || dropped != 0 {
		t.Fatalf("expected no-op under budget, got dropped=%d truncated=%v", dropped, truncated)
	}
}

// GAP-D4-10: dropping an assistant's tool_use message during truncation
// must also drop its immediately-following paired tool_result message
// in the SAME step — leaving one without the other produces a fresh
// orphaned functionResponse/functionCall mismatch with nothing
// downstream left to repair it (truncation runs AFTER sanitize's own
// graph-consistency repair in the real pipeline, not before).
func TestTruncateConversationToFit_DropsToolUseAndToolResultTogether(t *testing.T) {
	big := strings.Repeat("x ", 5000)
	reqJSON := `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "original task"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "ok"}]},
			{"role": "assistant", "content": "` + big + `"},
			{"role": "user", "content": "` + big + `"},
			{"role": "assistant", "content": "` + big + `"},
			{"role": "user", "content": "` + big + `"}
		]
	}`
	req := mustReq(t, reqJSON)
	newReq, dropped, truncated := TruncateConversationToFit(req, 2000, 100)
	if !truncated || dropped == 0 {
		t.Fatalf("expected truncation to fire, got dropped=%d truncated=%v", dropped, truncated)
	}
	toolUseStillPresent := false
	toolResultStillPresent := false
	for _, m := range newReq.Messages {
		for _, b := range m.Content {
			if tu, ok := b.AsToolUse(); ok && tu.ID == "t1" {
				toolUseStillPresent = true
			}
			if tr, ok := b.AsToolResult(); ok && tr.ToolUseID == "t1" {
				toolResultStillPresent = true
			}
		}
	}
	if toolUseStillPresent != toolResultStillPresent {
		t.Fatalf("expected tool_use and its paired tool_result to be dropped together, got toolUsePresent=%v toolResultPresent=%v", toolUseStillPresent, toolResultStillPresent)
	}
}

func TestImageCost_AllSVGMediaTypesPricedAsText(t *testing.T) {
	svg := strings.Repeat("<svg>path data here</svg>", 50)
	types := []string{
		"image/svg+xml",
		"image/svg",
		"image/x-svg",
		"image/svg-xml",
		"IMAGE/SVG+XML; charset=utf-8",
		"image/svg; foo=bar",
		"IMAGE/X-SVG",
	}
	want := len(svg) * 3 / 4 / 2
	for _, mt := range types {
		src := claude.ImageSource{Type: "base64", MediaType: mt, Data: svg}
		got := imageCost(src, WeightedCharTokens)
		if got != want {
			t.Errorf("imageCost for %q: got %d, want %d (must price as text, not raster floor 1600)", mt, got, want)
		}

		docSrc := claude.DocumentSource{Type: "base64", MediaType: mt, Data: svg}
		docGot := documentCost(docSrc, WeightedCharTokens)
		if docGot != want {
			t.Errorf("documentCost for %q: got %d, want %d (must price as text, not raster floor 1600)", mt, docGot, want)
		}
	}
}

func TestEstimateImageTokens_MaxTileCappingHardwareCeiling(t *testing.T) {
	makeWebP := func(w, h int) string {
		buf := make([]byte, 30)
		copy(buf[0:4], "RIFF")
		copy(buf[8:12], "WEBP")
		copy(buf[12:16], "VP8L")
		buf[20] = 0x2f
		val := uint32(w-1)&0x3FFF | (uint32(h-1)&0x3FFF)<<14
		buf[21] = byte(val)
		buf[22] = byte(val >> 8)
		buf[23] = byte(val >> 16)
		buf[24] = byte(val >> 24)
		return base64.StdEncoding.EncodeToString(buf)
	}

	testCases := []struct {
		w, h int
	}{
		{4000, 4000},
		{6000, 3000},
		{8000, 2000},
		{10000, 1000},
		{3000, 3000},
	}
	for _, tc := range testCases {
		tokens := estimateImageTokens(makeWebP(tc.w, tc.h))
		if tokens > 2064 {
			t.Errorf("dimensions (%d, %d) produced %d tokens, exceeding 8-tile ceiling of 2064 tokens", tc.w, tc.h, tokens)
		}
	}
}

func TestEstimatePDFTokensFromPageCount_NestedDictionaries(t *testing.T) {
	// Nested sub-dictionary before /Count
	raw1 := []byte("%PDF-1.4\n<< /Type /Pages /Resources << /Font << /F1 4 0 R >> >> /Count 12 >>")
	b64_1 := base64.StdEncoding.EncodeToString(raw1)
	got1, ok1 := estimatePDFTokensFromPageCount(b64_1)
	if !ok1 {
		t.Fatalf("expected page count to be found for nested dictionary")
	}
	want1 := 12 * geminiPDFTokensPerPage
	if got1 != want1 {
		t.Fatalf("got %d tokens, want %d", got1, want1)
	}

	// Reversed order with nested sub-dictionary
	raw2 := []byte("%PDF-1.4\n<< /Count 15 /Resources << /ProcSet [ /PDF ] >> /Type /Pages >>")
	b64_2 := base64.StdEncoding.EncodeToString(raw2)
	got2, ok2 := estimatePDFTokensFromPageCount(b64_2)
	if !ok2 {
		t.Fatalf("expected page count to be found for reversed dictionary with nested elements")
	}
	want2 := 15 * geminiPDFTokensPerPage
	if got2 != want2 {
		t.Fatalf("got %d tokens, want %d", got2, want2)
	}
}

func TestTruncateConversationToFit_VirtualReadDeduplication(t *testing.T) {
	// A file read repeated within 10 turns would take ~25,000 tokens raw each,
	// exceeding budget (30,000). But with virtual read dedup, the second read
	// is estimated at ~82 tokens, keeping total well under budget and avoiding truncation.
	largeContent := strings.Repeat("line of code in file\n", 2500) // ~50KB
	largeContentJSON, _ := json.Marshal(largeContent)

	reqJSON := `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "read the file"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "r1", "name": "Read", "input": {"file_path": "main.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "r1", "content": ` + string(largeContentJSON) + `}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "r2", "name": "Read", "input": {"file_path": "main.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "r2", "content": ` + string(largeContentJSON) + `}]},
			{"role": "assistant", "content": "done"}
		]
	}`
	req := mustReq(t, reqJSON)
	// Budget of 35000 tokens: large enough for 1 raw read (~25k) + 1 stub (~82),
	// but too small for 2 raw reads (~50k).
	_, dropped, truncated := TruncateConversationToFit(req, 38000, 100)
	if truncated || dropped > 0 {
		t.Fatalf("expected virtual read dedup to prevent truncation under budget, got dropped=%d truncated=%v", dropped, truncated)
	}
}

func TestTruncateConversationToFit_SyntheticAssistantTurnOnUserBoundary(t *testing.T) {
	// Test when i stops at len(req.Messages)-2 and req.Messages[len-2].Role == RoleUser.
	// Must insert a synthetic assistant transition turn so two consecutive user turns are not produced.
	big := strings.Repeat("x ", 500)
	reqJSON := `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "original user request"},
			{"role": "assistant", "content": "` + big + `"},
			{"role": "user", "content": "` + big + `"},
			{"role": "assistant", "content": "` + big + `"},
			{"role": "user", "content": "penultimate user turn"},
			{"role": "assistant", "content": "final assistant turn"}
		]
	}`
	req := mustReq(t, reqJSON)
	// Low max tokens forcing truncation down to the last 2 messages (len-2, len-1)
	newReq, dropped, truncated := TruncateConversationToFit(req, 500, 100)
	if !truncated || dropped == 0 {
		t.Fatalf("expected truncation, got dropped=%d", dropped)
	}

	// Message alternation must be preserved: no two consecutive user turns
	for i := 0; i < len(newReq.Messages)-1; i++ {
		if newReq.Messages[i].Role == newReq.Messages[i+1].Role {
			t.Fatalf("consecutive same role %q at indices %d and %d", newReq.Messages[i].Role, i, i+1)
		}
	}
	// Index 1 must be synthetic assistant turn
	if len(newReq.Messages) < 4 {
		t.Fatalf("expected at least 4 messages (user, synthetic assistant, user, assistant), got %d", len(newReq.Messages))
	}
	if newReq.Messages[0].Role != claude.RoleUser {
		t.Errorf("msg 0 role = %s, want user", newReq.Messages[0].Role)
	}
	if newReq.Messages[1].Role != claude.RoleAssistant {
		t.Errorf("msg 1 role = %s, want assistant (synthetic transition turn)", newReq.Messages[1].Role)
	}
	if newReq.Messages[2].Role != claude.RoleUser {
		t.Errorf("msg 2 role = %s, want user", newReq.Messages[2].Role)
	}
}

func TestAdversarial_PDF_Corrupted_Encrypted_NoPages(t *testing.T) {
	// 1. Completely empty PDF
	if got, ok := estimatePDFTokensFromPageCount(""); ok || got != 0 {
		t.Errorf("empty string: got (%d, %v), want (0, false)", got, ok)
	}

	// 2. Encrypted PDF without plaintext /Pages
	encPDF := "%PDF-1.4\n1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n2 0 obj << /Filter /Standard /V 2 /R 3 /O (secret) /U (secret) /P -4 >> endobj"
	encB64 := base64.StdEncoding.EncodeToString([]byte(encPDF))
	if got, ok := estimatePDFTokensFromPageCount(encB64); ok {
		t.Errorf("encrypted PDF should not return trusted page count, got (%d, %v)", got, ok)
	}

	// 3. Corrupted PDF with infinite unclosed angle brackets
	unclosed := strings.Repeat("<<", 1000) + "/Type /Pages /Count 5"
	unclosedB64 := base64.StdEncoding.EncodeToString([]byte(unclosed))
	// Must not panic, and fallback should still match or safely return
	_, _ = estimatePDFTokensFromPageCount(unclosedB64)

	// 4. Corrupted PDF with unbalanced closing angle brackets
	unbalancedClose := "%PDF-1.4\n>>>>>> << /Type /Pages /Count 5 >>"
	unbalancedB64 := base64.StdEncoding.EncodeToString([]byte(unbalancedClose))
	if got, ok := estimatePDFTokensFromPageCount(unbalancedB64); !ok || got != 5*geminiPDFTokensPerPage {
		t.Errorf("unbalanced close brackets: got (%d, %v), want (1290, true)", got, ok)
	}

	// 5. PDF with /Count 0 (must not be treated as valid 0 pages)
	zeroPages := "%PDF-1.4\n<< /Type /Pages /Count 0 >>"
	zeroB64 := base64.StdEncoding.EncodeToString([]byte(zeroPages))
	if got, ok := estimatePDFTokensFromPageCount(zeroB64); ok {
		t.Errorf("zero count must return ok=false, got (%d, %v)", got, ok)
	}

	// 6. PDF with negative /Count -10 (regex (\d+) won't match, so no false pages)
	negPages := "%PDF-1.4\n<< /Type /Pages /Count -10 >>"
	negB64 := base64.StdEncoding.EncodeToString([]byte(negPages))
	if got, ok := estimatePDFTokensFromPageCount(negB64); ok {
		t.Errorf("negative count must return ok=false, got (%d, %v)", got, ok)
	}

	// 7. Non-numeric /Count string
	strPages := "%PDF-1.4\n<< /Type /Pages /Count (five) >>"
	strB64 := base64.StdEncoding.EncodeToString([]byte(strPages))
	if got, ok := estimatePDFTokensFromPageCount(strB64); ok {
		t.Errorf("string count must return ok=false, got (%d, %v)", got, ok)
	}

	// 8. Document cost fallback for corrupted/encrypted PDF: floor check
	docSrc := claude.DocumentSource{
		Type:      "base64",
		MediaType: "application/pdf",
		Data:      base64.StdEncoding.EncodeToString([]byte("corrupted pdf")),
	}
	cost := documentCost(docSrc, WeightedCharTokens)
	if cost != minPDFTokenFloor {
		t.Errorf("corrupted PDF token cost = %d, want floor %d", cost, minPDFTokenFloor)
	}
}

func TestAdversarial_SVG_Handling_BillingAndMIMEs(t *testing.T) {
	svgMIMEs := []string{
		"image/svg+xml",
		"IMAGE/SVG+XML",
		"image/svg+xml; charset=utf-8",
		"image/svg",
		"image/x-svg",
		"image/svg-xml",
	}

	for _, mt := range svgMIMEs {
		if !IsSVGMediaType(mt) {
			t.Errorf("IsSVGMediaType(%q) = false, want true", mt)
		}
	}

	// Non-SVGs
	nonSVGs := []string{"image/png", "image/jpeg", "image/gif", "image/webp", "application/pdf"}
	for _, mt := range nonSVGs {
		if IsSVGMediaType(mt) {
			t.Errorf("IsSVGMediaType(%q) = true, want false", mt)
		}
	}

	// Cost verification: SVG billed as text (decodedBytes / 2), NOT as raster tile image
	svgXML := `<svg width="100" height="100"><circle cx="50" cy="50" r="40" stroke="green" fill="yellow" /></svg>`
	svgB64 := base64.StdEncoding.EncodeToString([]byte(svgXML))
	imgSrc := claude.ImageSource{
		Type:      "base64",
		MediaType: "image/svg+xml",
		Data:      svgB64,
	}
	gotCost := imageCost(imgSrc, WeightedCharTokens)
	expectedDecodedBytes := len(svgB64) * 3 / 4
	expectedCost := expectedDecodedBytes / 2
	if gotCost != expectedCost {
		t.Errorf("SVG cost = %d, want %d (decodedBytes / 2)", gotCost, expectedCost)
	}
}

// -----------------------------------------------------------------------------
// Tokenomics & Context Window Truncation Audit Tests
// -----------------------------------------------------------------------------

func TestTokenAccuracy_CategoriesBreakdown(t *testing.T) {
	goCode := `package tokens
import ("encoding/json"; "math"; "unicode/utf8")
func WeightedCharTokens(text string) int {
	if text == "" { return 0 }
	length := utf8.RuneCountInString(text)
	if length < 5 { return int(math.Ceil(float64(length) / 3.0)) }
	var regularAscii, digits, symbols, nonASCII, cjk int
	for _, r := range text {
		switch {
		case isCJK(r): cjk++
		case r >= 0x80: nonASCII++
		default: regularAscii++
		}
	}
	return int(math.Ceil(float64(regularAscii)/4.5 + float64(digits)/2.0 + float64(symbols)/1.5 + float64(nonASCII)/1.5 + float64(cjk)/1.25))
}`
	pyCode := `import os, sys
from typing import List, Dict, Optional
class TokenCounter:
    def __init__(self, model: str = "gemini-2.5-pro"):
        self.model = model
        self.cache: Dict[str, int] = {}
    def count(self, text: str) -> int:
        return len(text.split()) * 4 // 3
`
	tsCode := `import React, { useState, useMemo } from 'react';
interface Props { id: string; role: 'user'|'assistant'; content: string; }
export const View: React.FC<Props> = ({ id, role, content }) => {
    const [open, setOpen] = useState(false);
    return <div onClick={() => setOpen(!open)}>{content}</div>;
};`

	bashHeredoc := `#!/usr/bin/env bash
set -euo pipefail
cat << 'EOF' > /etc/systemd/system/claudgravity.service
[Unit]
Description=ClaudGravity Gateway Service v2
After=network.target
[Service]
Type=simple
User=relay
WorkingDirectory=/opt/claudgravitynext
ExecStart=/opt/claudgravitynext/claudgravity -config /opt/claudgravitynext/config.json
Restart=always
RestartSec=5s
Environment="PORT=8901"
Environment="TLS_PORT=8903"
LimitNOFILE=65536
[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload && systemctl enable --now claudgravity.service`

	russianText := `Твоя цель: Проверить точность токенизации, математику усечения контекстного окна, эмуляцию кэша промптов и алгоритмы дедупликации повторных чтений.
Исследуй точность токенизатора internal/tokens (estimate.go, text.go, blockcost.go).
Сравни оценку WeightedRequestTokens с реальными данными Gemini usageMetadata.promptTokenCount.
Входит ли отношение driftRatio в диапазон [1.00, 1.30] для не менее 95% запросов?`

	toolSchema := `{
		"name": "edit_file",
		"description": "Performs exact string replacement in a file. You must Read the file in this conversation before editing, or the call will fail.",
		"input_schema": {
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "The absolute path to the file to modify"},
				"old_string": {"type": "string", "description": "The text to replace"},
				"new_string": {"type": "string", "description": "The replacement string"}
			},
			"required": ["file_path", "old_string", "new_string"]
		}
	}`

	claudeMD := `# ClaudGravityNext — entry point for any agent working here
This is the v2 rewrite of the gateway that lives at ../superrelay (v1, still the live production codebase).
Target 80–250 lines per file, hard ceiling 400, enforced by internal/ci/filesize_test.go once that exists.`

	samples := []struct {
		category string
		text     string
	}{
		{"Go Code", goCode},
		{"Python Code", pyCode},
		{"TypeScript Code", tsCode},
		{"Bash Heredoc", bashHeredoc},
		{"Russian Text", russianText},
		{"Tool Schema", toolSchema},
		{"CLAUDE.md System", claudeMD},
	}

	for _, s := range samples {
		toks := WeightedCharTokens(s.text)
		runes := len([]rune(s.text))
		if toks <= 0 {
			t.Errorf("[%s] expected positive token count, got %d", s.category, toks)
		}
		ratio := float64(toks) / float64(runes)
		t.Logf("[%s] runes=%d, toks=%d, ratio=%.3f toks/rune (%.2f runes/tok)", s.category, runes, toks, ratio, 1.0/ratio)
		if s.category == "Russian Text" {
			// Russian text with mixed ASCII technical terms yields ~0.45 - 0.55 toks/rune
			if ratio < 0.40 || ratio > 0.70 {
				t.Errorf("Russian text token/rune ratio unexpected: %.3f", ratio)
			}
		} else if s.category == "CLAUDE.md System" {
			if ratio > 0.40 {
				t.Errorf("English prose token/rune ratio unexpectedly high: %.3f", ratio)
			}
		}
	}
}

func TestTruncateConversationToFit_PreservesSystemAndToolsIntegrity(t *testing.T) {
	hugeSys := strings.Repeat("System instructions and guidelines. ", 500)
	sysJSON, _ := json.Marshal(hugeSys)
	toolSchemaJSON := json.RawMessage(`{"type":"object","properties":{"arg":{"type":"string"}}}`)
	tools := []claude.Tool{
		{Name: "ToolA", Description: "Description of tool A", InputSchema: toolSchemaJSON},
		{Name: "ToolB", Description: "Description of tool B", InputSchema: toolSchemaJSON},
	}

	big := strings.Repeat("x ", 2000)
	req := claude.Request{
		Model:  "gemini-2.5-flash",
		System: claude.SystemPrompt(sysJSON),
		Tools:  tools,
		Messages: []claude.Message{
			{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: "Initial user task"})}},
			{Role: claude.RoleAssistant, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: big})}},
			{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: big})}},
			{Role: claude.RoleAssistant, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: big})}},
			{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: "Recent question"})}},
			{Role: claude.RoleAssistant, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: "Recent answer"})}},
		},
	}

	newReq, dropped, truncated := TruncateConversationToFit(req, 2000, 100)
	if !truncated || dropped == 0 {
		t.Fatalf("expected truncation, got dropped=%d", dropped)
	}

	// System prompt must remain 100% byte-identical
	if string(newReq.System) != string(req.System) {
		t.Errorf("system prompt was mutated or truncated!")
	}
	// Tools must remain 100% identical in length and properties
	if len(newReq.Tools) != len(req.Tools) {
		t.Errorf("tools count changed from %d to %d", len(req.Tools), len(newReq.Tools))
	}
	for i := range newReq.Tools {
		if newReq.Tools[i].Name != req.Tools[i].Name {
			t.Errorf("tool %d name changed: %q vs %q", i, newReq.Tools[i].Name, req.Tools[i].Name)
		}
	}
}

func TestTruncateConversationToFit_OrphanedToolResultHandledSafely(t *testing.T) {
	big := strings.Repeat("x ", 500)
	req := claude.Request{
		Model: "gemini-2.5-flash",
		Messages: []claude.Message{
			{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: "Original task"})}},
			{Role: claude.RoleAssistant, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: big})}},
			{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.ToolResultBlock{Type: "tool_result", ToolUseID: "orphaned_call_id", Content: json.RawMessage(`"orphaned result"`), IsError: false})}},
			{Role: claude.RoleAssistant, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: big})}},
			{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: "latest user turn"})}},
			{Role: claude.RoleAssistant, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: "latest assistant turn"})}},
		},
	}

	newReq, dropped, truncated := TruncateConversationToFit(req, 800, 100)
	if !truncated || dropped == 0 {
		t.Fatalf("expected truncation, got dropped=%d", dropped)
	}

	// Verify alternation
	for i := 0; i < len(newReq.Messages)-1; i++ {
		if newReq.Messages[i].Role == newReq.Messages[i+1].Role {
			t.Fatalf("broken alternation after truncating orphaned tool_result: msg %d and %d both %s", i, i+1, newReq.Messages[i].Role)
		}
	}
}

