package tokens

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

// Regression: Gemini 3.x bills images at a flat ~1088 tokens (measured
// live for 32px..1024px). Reverting the ImageMinTokens floor makes small
// images price by area (e.g. 32x32 -> 2 tokens).
func TestRegress_ImageProfileFlat1088(t *testing.T) {
	for _, d := range [][2]int{{32, 32}, {64, 48}, {200, 200}, {512, 512}, {1024, 768}} {
		if got := estimateImageTokensP(pngB64(t, d[0], d[1]), Gemini3Profile); got != 1088 {
			t.Errorf("%dx%d = %d, want flat 1088", d[0], d[1], got)
		}
	}
	if Gemini3Profile.ImageMinTokens != 1088 || Gemini3Profile.ImageMaxTokens != 1120 {
		t.Errorf("profile constants changed: %+v", Gemini3Profile)
	}
}

func pdfB64(pages int) string {
	body := fmt.Sprintf("%%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n2 0 obj\n<< /Type /Pages /Kids [] /Count %d >>\nendobj\ntrailer\n<< /Root 1 0 R >>\n%%%%EOF\n", pages)
	return base64.StdEncoding.EncodeToString([]byte(body))
}

// Regression: Gemini 3.x PDF pages are ~530 tokens each (not 258).
func TestRegress_PDFPagesPriced530(t *testing.T) {
	for _, pages := range []int{1, 3, 10} {
		got, ok := estimatePDFTokensP(pdfB64(pages), Gemini3Profile)
		if !ok || got != pages*530 {
			t.Errorf("%d pages: got %d ok=%v, want %d", pages, got, ok, pages*530)
		}
	}
	if got, _ := estimatePDFTokensP(pdfB64(2), Gemini2Profile); got != 2*258 {
		t.Errorf("gemini 2.x must keep 258/page, got %d", got)
	}
	if ProfileForModel("gemini-3.8-flash").PDFTokensPerPage != 530 {
		t.Error("default family must be 3.x")
	}
}

func nestedShot(size int) string {
	return fmt.Sprintf(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}`, strings.Repeat("A", size))
}

func reqWithNestedShots(sizes ...int) string {
	var msgs []string
	for i, sz := range sizes {
		msgs = append(msgs,
			fmt.Sprintf(`{"role":"assistant","content":[{"type":"tool_use","id":"t%d","name":"Shot","input":{}}]}`, i),
			fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d","content":[%s]}]}`, i, nestedShot(sz)))
	}
	return `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"go"},` + strings.Join(msgs, ",") + `]}`
}

// Regression: the pruning threshold is 18MiB inclusive. Exactly at the
// limit nothing is pruned; one byte over prunes the oldest; the constant
// itself must stay under the 20MB upstream inlineData ceiling.
func TestRegress_PruneAttachmentsThreshold(t *testing.T) {
	if MaxInlineAttachmentBytes != 18<<20 || MaxInlineAttachmentBytes >= 20<<20 {
		t.Fatalf("threshold constant changed: %d", MaxInlineAttachmentBytes)
	}
	third := MaxInlineAttachmentBytes / 3
	at := mustRequest(t, reqWithNestedShots(third, third, MaxInlineAttachmentBytes-2*third))
	if InlineAttachmentBytes(at) != MaxInlineAttachmentBytes {
		t.Fatalf("setup: %d", InlineAttachmentBytes(at))
	}
	if _, n := PruneAttachmentsByBytes(at, MaxInlineAttachmentBytes); n != 0 {
		t.Fatalf("exactly at the limit pruned %d", n)
	}
	over := mustRequest(t, reqWithNestedShots(third, third, MaxInlineAttachmentBytes-2*third+1))
	pruned, n := PruneAttachmentsByBytes(over, MaxInlineAttachmentBytes)
	if n != 1 {
		t.Fatalf("one byte over: pruned %d, want 1 (the oldest)", n)
	}
	if InlineAttachmentBytes(pruned) > MaxInlineAttachmentBytes {
		t.Fatal("still over after pruning")
	}
	// the oldest nested screenshot is the one replaced
	if strings.Contains(string(pruned.Messages[2].Content[0].Raw), `"type":"image"`) ||
		!strings.Contains(string(pruned.Messages[len(pruned.Messages)-1].Content[0].Raw), `"type":"image"`) {
		t.Fatal("must prune the oldest and keep the newest")
	}
}
