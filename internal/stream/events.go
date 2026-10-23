// Package stream ports superrelay (v1)'s sse.go (1225 lines) — the
// SSEStreamer that translates Gemini's streaming chunks into Anthropic
// Messages API SSE events — rewritten around an explicit state machine
// per the migration spec's Part 6.3, not a zero-diff port: v1's
// NewSSEStreamer took 15 positional parameters (the same Part 1.4 failure
// mode internal/reqctx exists to fix elsewhere), and GAP-D1-01/GAP-D1-02
// (a double-written response after headers were already sent; the
// streaming path's own prefill application being silently forgotten
// where the non-streaming path's wasn't) were both real incidents rooted
// in this file having no single, explicit point where "have headers gone
// out yet" or "has prefill been applied yet" was decided.
//
// This file holds the low-level SSE wire-format helpers — pure string
// building and the one write+flush primitive — kept separate from
// safewriter.go's locking/state-tracking logic.
package stream

import (
	"fmt"
	"net/http"
)

func formatSSEEvent(event, data string) string {
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event, data)
}

func formatSSEKeepalivePing() string {
	return "event: ping\ndata: {\"type\":\"ping\"}\n\n"
}

// writeAndFlush writes raw to w and flushes immediately if flusher is
// non-nil — the one place a byte actually leaves this process toward the
// client.
func writeAndFlush(w http.ResponseWriter, flusher http.Flusher, raw string) error {
	_, err := w.Write([]byte(raw))
	if err == nil && flusher != nil {
		flusher.Flush()
	}
	return err
}
