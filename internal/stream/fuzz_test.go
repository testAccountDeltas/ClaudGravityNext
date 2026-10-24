package stream

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/reqctx"
)

// FuzzStreamGeminiToClaude feeds arbitrary bytes as the upstream SSE body.
// Whatever the upstream sends, the reader must not panic or hang, and every
// frame already written to the client must be well formed: valid JSON data,
// content blocks opened before they get deltas and closed at most once, and
// nothing after message_stop.
func FuzzStreamGeminiToClaude(f *testing.F) {
	seeds := []string{
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\"}]}\n\n",
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"thought\":true,\"text\":\"t\"},{\"text\":\"a\"}]}}]}\n\ndata: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"b\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2}}\n\n",
		"data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"Read\",\"args\":{\"file_path\":\"a\"}},\"thoughtSignature\":\"sig\"},{\"functionCall\":{\"name\":\"Read\",\"args\":{}}}]},\"finishReason\":\"STOP\"}]}\n\n",
		"data: {\"response\":{\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"wrapped\"}]},\"finishReason\":\"MAX_TOKENS\"}]}}\n\n",
		"data: {\"error\":{\"code\":429,\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n",
		"data: [DONE]\n\n", "data: {\n\n", "data:\n\n", ": comment\n\n", "event: x\ndata: 1\n\n", "\r\n\r\n", "data: null\n\n",
		"data: {\"candidates\":[]}\n\ndata: {\"candidates\":[{\"finishReason\":\"SAFETY\"}]}\n\n",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, body string) {
		rec := httptest.NewRecorder()
		w := NewSafeSSEWriter(1, rec, rec, nil)
		fsm := NewFSM(w, 1, &reqctx.Params{MaxTokens: 1000}, &reqctx.State{})
		_ = StreamGeminiToClaude(fsm, strings.NewReader(body), nil)
		checkWireOrder(t, rec.Body.String())
	})
}

func checkWireOrder(t *testing.T, out string) {
	t.Helper()
	open := map[int64]bool{}
	closed := map[int64]bool{}
	stopped := false
	for _, e := range parseEvents(out) {
		if stopped {
			t.Fatalf("event %q after message_stop:\n%s", e.Name, out)
		}
		if e.Data != "" && !gjson.Valid(e.Data) {
			t.Fatalf("event %q has invalid JSON data %q", e.Name, e.Data)
		}
		idx := gjson.Get(e.Data, "index").Int()
		switch e.Name {
		case "content_block_start":
			if open[idx] || closed[idx] {
				t.Fatalf("block %d started twice:\n%s", idx, out)
			}
			open[idx] = true
		case "content_block_delta":
			if !open[idx] {
				t.Fatalf("delta for block %d that is not open:\n%s", idx, out)
			}
		case "content_block_stop":
			if !open[idx] {
				t.Fatalf("stop for block %d that is not open:\n%s", idx, out)
			}
			open[idx], closed[idx] = false, true
		case "message_stop":
			stopped = true
			for i, o := range open {
				if o {
					t.Fatalf("message_stop with block %d still open:\n%s", i, out)
				}
			}
		}
	}
}
