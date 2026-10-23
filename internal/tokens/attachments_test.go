package tokens

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"strings"
	"testing"

	"claudgravity/internal/protocol/claude"
)

func pngB64(t *testing.T, w, h int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func screenshotItem(sizeBytes int) string {
	return fmt.Sprintf(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}`, strings.Repeat("A", sizeBytes))
}

func mustRequest(t *testing.T, raw string) claude.Request {
	t.Helper()
	var req claude.Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	return req
}

// 120 screenshots nested in tool_result blocks (24MB) must be pruned
// oldest-first below the limit, keeping the newest.
func TestPruneAttachmentsByBytes_NestedToolResultScreenshots(t *testing.T) {
	var msgs []string
	for i := 0; i < 12; i++ {
		var items []string
		for j := 0; j < 10; j++ {
			items = append(items, screenshotItem(200_000))
		}
		items = append(items, `{"type":"text","text":"done"}`)
		msgs = append(msgs,
			fmt.Sprintf(`{"role":"assistant","content":[{"type":"tool_use","id":"t%d","name":"Screenshot","input":{}}]}`, i),
			fmt.Sprintf(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t%d","content":[%s]}]}`, i, strings.Join(items, ",")))
	}
	req := mustRequest(t, `{"model":"m","max_tokens":10,"messages":[{"role":"user","content":"go"},`+strings.Join(msgs, ",")+`]}`)
	before := InlineAttachmentBytes(req)
	if before != 120*200_000 {
		t.Fatalf("setup: %d", before)
	}
	pruned, n := PruneAttachmentsByBytes(req, MaxInlineAttachmentBytes)
	if n == 0 {
		t.Fatal("expected pruning")
	}
	if after := InlineAttachmentBytes(pruned); after > MaxInlineAttachmentBytes {
		t.Fatalf("still %d bytes after pruning", after)
	}
	if InlineAttachmentBytes(req) != before {
		t.Fatal("input request was mutated")
	}
	raw, _ := json.Marshal(pruned)
	if !strings.Contains(string(raw), "attachment omitted") {
		t.Fatal("placeholder missing")
	}
	// newest tool_result keeps all its screenshots; oldest lost its images
	last := pruned.Messages[len(pruned.Messages)-1]
	if got := strings.Count(string(last.Content[0].Raw), `"type":"image"`); got != 10 {
		t.Fatalf("newest tool_result lost images: %d", got)
	}
	first := string(pruned.Messages[2].Content[0].Raw)
	if strings.Contains(first, `"type":"image"`) {
		t.Fatal("oldest tool_result kept images")
	}
	if !strings.Contains(first, `"tool_use_id":"t0"`) || !strings.Contains(first, `"done"`) {
		t.Fatalf("tool_result structure damaged: %s", first[:200])
	}
}

func TestPruneAttachmentsByBytes_TopLevelAndSingleOversizedKept(t *testing.T) {
	big := fmt.Sprintf(`{"type":"image","source":{"type":"base64","media_type":"image/png","data":%q}}`, strings.Repeat("A", 10<<20))
	req := mustRequest(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[`+big+`]},{"role":"assistant","content":"ok"},{"role":"user","content":[`+big+`,`+big+`]}]}`)
	pruned, n := PruneAttachmentsByBytes(req, MaxInlineAttachmentBytes)
	if n != 2 {
		t.Fatalf("pruned %d, want 2", n)
	}
	if got := InlineAttachmentBytes(pruned); got != 10<<20 {
		t.Fatalf("remaining %d", got)
	}
	// a lone oversized attachment is never pruned (caller answers 413)
	one := mustRequest(t, `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[`+big+`]}]}`)
	if _, n := PruneAttachmentsByBytes(one, 1<<20); n != 0 {
		t.Fatal("single attachment must be kept")
	}
}

func TestImageAndPDFProfiles(t *testing.T) {
	g3 := Gemini3Profile
	if got := estimateImageTokensP(pngB64(t, 1024, 768), g3); got < 1040 || got > 1060 {
		t.Fatalf("1024x768 on 3.x = %d, want ~1048", got)
	}
	if got := estimateImageTokensP(pngB64(t, 200, 200), g3); got < 50 || got > 56 {
		t.Fatalf("200x200 on 3.x = %d, want ~53", got)
	}
	if got := estimateImageTokensP(pngB64(t, 4000, 3000), g3); got != 1120 {
		t.Fatalf("large image on 3.x = %d, want cap 1120", got)
	}
	if got := estimateImageTokensP("not-an-image", g3); got != 1120 {
		t.Fatalf("unknown dims on 3.x = %d, want 1120", got)
	}
	// 2.x keeps tile pricing
	if got := estimateImageTokensP(pngB64(t, 1024, 768), Gemini2Profile); got != 2*258 {
		t.Fatalf("1024x768 on 2.x = %d, want %d", got, 2*258)
	}
	if ProfileForModel("gemini-2.5-pro").PDFTokensPerPage != 258 || ProfileForModel("gemini-3.8-flash").PDFTokensPerPage != 560 {
		t.Fatal("ProfileForModel family selection wrong")
	}
}

func TestContextBudget(t *testing.T) {
	if got := ContextBudget(1_048_576, 64_000); got != 1_048_576-64_000-ContextSafetyMarginTokens {
		t.Fatalf("budget %d", got)
	}
}
