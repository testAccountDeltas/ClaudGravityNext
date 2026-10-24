package stream

import (
	"fmt"
	"net/http/httptest"
	"strings"

	"github.com/tidwall/gjson"

	"claudgravity/internal/reqctx"
)

// sseEvent is one parsed "event:/data:" frame of the Claude SSE output.
type sseEvent struct {
	Name string
	Data string
}

func parseEvents(body string) []sseEvent {
	var out []sseEvent
	for _, frame := range strings.Split(body, "\n\n") {
		var ev sseEvent
		for _, line := range strings.Split(frame, "\n") {
			switch {
			case strings.HasPrefix(line, "event:"):
				ev.Name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				ev.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			}
		}
		if ev.Name != "" {
			out = append(out, ev)
		}
	}
	return out
}

func eventNames(body string) []string {
	var n []string
	for _, e := range parseEvents(body) {
		n = append(n, e.Name)
	}
	return n
}

// textOf concatenates every text_delta in the output.
func textOf(body string) string {
	var sb strings.Builder
	for _, e := range parseEvents(body) {
		if gjson.Get(e.Data, "delta.type").String() == "text_delta" {
			sb.WriteString(gjson.Get(e.Data, "delta.text").String())
		}
	}
	return sb.String()
}

func stopOf(body string) (reason, seq string) {
	d := parseEventPayload(body, "message_delta")
	return gjson.Get(d, "delta.stop_reason").String(), gjson.Get(d, "delta.stop_sequence").String()
}

// feedText drives a fresh FSM with one text chunk per element (the last
// one carries finishReason STOP) through the real SSE reader.
func feedText(p *reqctx.Params, texts ...string) (*FSM, string, error) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	w.HoldEvents()
	f := NewFSM(w, 1, p, &reqctx.State{})
	var sb strings.Builder
	for i, t := range texts {
		fr := ""
		if i == len(texts)-1 {
			fr = "STOP"
		}
		// No usageMetadata: the visible-cap estimator must be the one deciding.
		c := fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":%q}]}`, t)
		if fr != "" {
			c += `,"finishReason":"` + fr + `"`
		}
		fmt.Fprintf(&sb, "data: %s}]}\n\n", c)
	}
	err := StreamGeminiToClaude(f, strings.NewReader(sb.String()), nil)
	return f, rec.Body.String(), err
}

type readerFunc func([]byte) (int, error)

func (r readerFunc) Read(p []byte) (int, error) { return r(p) }

var reqctxState reqctx.State

func newHeld() (*httptest.ResponseRecorder, *SafeSSEWriter) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	w.HoldEvents()
	return rec, w
}

// feedChunks drives a held FSM with raw Gemini chunks via ProcessGeminiChunk.
func feedChunks(p *reqctx.Params, chunks ...[]byte) (*FSM, string) {
	f, rec, _ := heldFSM(p)
	for _, c := range chunks {
		if err := f.ProcessGeminiChunk(c); err != nil {
			break
		}
	}
	return f, rec.Body.String()
}

// blockSummary lists the content blocks of an SSE body in order as
// "type" or "tool_use:<name>", with the concatenated delta payload.
func blockSummary(body string) []string {
	var out []string
	idx := map[int]int{}
	for _, e := range parseEvents(body) {
		switch e.Name {
		case "content_block_start":
			cb := gjson.Get(e.Data, "content_block")
			s := cb.Get("type").String()
			if s == "tool_use" {
				s += ":" + cb.Get("name").String()
			}
			idx[int(gjson.Get(e.Data, "index").Int())] = len(out)
			out = append(out, s)
		}
	}
	return out
}

func q(s string) string { return fmt.Sprintf("%q", s) }
