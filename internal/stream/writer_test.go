package stream

import (
	"net/http/httptest"
	"strings"
	"testing"

	"claudgravity/internal/toolsig"
)

func TestWriteSSEError_ShapeAndEscaping(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteSSEError(rec, rec, "overloaded_error", "boom \"quoted\"\nline")
	evs := parseEvents(rec.Body.String())
	if len(evs) != 1 || evs[0].Name != "error" {
		t.Fatalf("events = %+v", evs)
	}
	if !strings.Contains(evs[0].Data, `"type":"overloaded_error"`) || !strings.Contains(evs[0].Data, `\"quoted\"\nline`) {
		t.Fatalf("payload must be valid escaped JSON: %s", evs[0].Data)
	}
}

func TestDiscardHeld_DropsBufferedEventsAndSilencesWriter(t *testing.T) {
	rec, w := newHeld()
	_ = w.EmitEvent("message_start", `{"type":"message_start"}`)
	_ = w.EmitEvent("content_block_start", `{}`)
	w.DiscardHeld()
	_ = w.EmitEvent("message_stop", `{"type":"message_stop"}`)
	if err := w.Release(); err != nil || rec.Body.Len() != 0 {
		t.Fatalf("a discarded attempt must put nothing on the wire: %q", rec.Body.String())
	}
	if w.HeadersSent() {
		t.Fatal("DiscardHeld must not commit headers")
	}
}

func TestMarkHeadersAlreadySent_TerminateWritesSSEErrorNotHTTP(t *testing.T) {
	rec := httptest.NewRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	if w.HeadersSent() {
		t.Fatal("fresh writer")
	}
	w.MarkHeadersAlreadySent()
	if !w.HeadersSent() {
		t.Fatal("MarkHeadersAlreadySent had no effect")
	}
	if sent := w.TerminateWithError("api_error", "late"); !sent {
		t.Fatal("headers were out, so the failure must be reported as an SSE event")
	}
	if !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if again := w.TerminateWithError("api_error", "twice"); !again || strings.Count(rec.Body.String(), "event: error") != 1 {
		t.Fatal("terminating twice must not write a second error")
	}
}

func TestGetToolSignature_RoundTripAndUnknown(t *testing.T) {
	setToolSignature("toolu_sig_rt", "sig-xyz")
	t.Cleanup(func() { setToolSignature("toolu_sig_rt", "") })
	if got := getToolSignature("toolu_sig_rt"); got != "sig-xyz" {
		t.Fatalf("got %q", got)
	}
	if got := getToolSignature("toolu_never_set"); got != "" {
		t.Fatalf("unknown id must be empty, got %q", got)
	}
	if toolsig.Get("toolu_sig_rt") != "sig-xyz" {
		t.Fatal("must share toolsig's store with the translate replay path")
	}
}
