package orchestrate

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// A direct-key request is answered with an early keepalive ping, which
// commits the 200. If the turn then turns out to be bad (empty), the failure
// can no longer be an HTTP status: Execute must close the stream with exactly
// one terminal SSE error event.
func TestExecute_BadTurnAfterEarlyPingEndsWithOneTerminalError(t *testing.T) {
	empty := tfSSE(tfWrap(`{"content":{"role":"model","parts":[]},"finishReason":"STOP"}`))
	r := runExec(t, execOpts{direct: true, streaming: true, panic: 1, ttft: 10,
		up: func(int, *http.Request, string) (*http.Response, error) {
			time.Sleep(1300 * time.Millisecond)
			return respBody(200, empty), nil
		}})
	if !r.ok {
		t.Fatalf("response is committed, ok must be true")
	}
	if n := strings.Count(r.body, "event: error"); n != 1 {
		t.Fatalf("want exactly one terminal error event, got %d:\n%s", n, r.body)
	}
	if strings.Contains(r.body, "message_stop") {
		t.Fatalf("an errored stream must not look successful:\n%s", r.body)
	}
}
