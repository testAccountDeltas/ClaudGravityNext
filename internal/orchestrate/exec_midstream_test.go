package orchestrate

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const firstChunk = "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"first words\"}]}}]}}\n\n"

func eventSeq(body string) []string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(l, "event: ") {
			out = append(out, l[7:])
		}
	}
	return out
}

// assertTerminalError checks the post-content failure contract: content
// block closed, one error event, nothing after it, never a message_stop.
func assertTerminalError(t *testing.T, body string) {
	t.Helper()
	ev := eventSeq(body)
	if len(ev) < 3 || ev[len(ev)-1] != "error" || ev[len(ev)-2] != "content_block_stop" {
		t.Fatalf("want ... content_block_stop, error (last), got %v", ev)
	}
	if strings.Contains(body, "message_stop") || strings.Count(body, "event: error") != 1 {
		t.Fatalf("no message_stop and exactly one error event expected: %s", body)
	}
	if !strings.Contains(body, "first words") {
		t.Fatalf("content sent before the failure must stay: %s", body)
	}
}

func TestMidStream_ReadErrorAfterContent_SSEErrorNotRetried(t *testing.T) {
	r := runExec(t, execOpts{streaming: true, up: func(n int, req *http.Request, _ string) (*http.Response, error) {
		return okResp(newCtxBody(req.Context(), "error", io.ErrUnexpectedEOF, firstChunk)), nil
	}})
	if !r.ok || r.attempts != 1 {
		t.Fatalf("content already sent: final response, no retry. ok=%v attempts=%d", r.ok, r.attempts)
	}
	assertTerminalError(t, r.body)
	if r.status == 200 || r.status == 499 {
		t.Fatalf("a dropped upstream is a server-side failure, got %d", r.status)
	}
}

func TestMidStream_WatchdogStall_Reports504(t *testing.T) {
	t.Cleanup(SetWatchdogForTest(5*time.Millisecond, 120*time.Millisecond))
	r := runExec(t, execOpts{streaming: true, up: func(n int, req *http.Request, _ string) (*http.Response, error) {
		return okResp(newCtxBody(req.Context(), "hang", nil, firstChunk)), nil
	}})
	if !r.ok || r.attempts != 1 || r.status != 504 {
		t.Fatalf("idle watchdog stall after content must be 504, not retried: ok=%v attempts=%d status=%d", r.ok, r.attempts, r.status)
	}
	assertTerminalError(t, r.body)
}

func TestMidStream_ClientCancelAfterContent_Reports499(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := runExec(t, execOpts{streaming: true, ctx: ctx, up: func(n int, req *http.Request, _ string) (*http.Response, error) {
		b := newCtxBody(req.Context(), "hang", nil, firstChunk)
		b.onHang = cancel // the client goes away while upstream is silent
		return okResp(b), nil
	}})
	if !r.ok || r.attempts != 1 || r.status != 499 {
		t.Fatalf("client disconnect must be 499 (not the watchdog's 504): ok=%v attempts=%d status=%d", r.ok, r.attempts, r.status)
	}
	if strings.Contains(r.body, "message_stop") {
		t.Fatalf("an aborted stream must not end cleanly: %s", r.body)
	}
	if ev := eventSeq(r.body); len(ev) > 0 && ev[len(ev)-1] == "error" && strings.Count(r.body, "event: error") != 1 {
		t.Fatalf("at most one error event: %v", ev)
	}
}

func TestMidStream_InBandErrorAfterContent_NoEventsAfterError(t *testing.T) {
	inband := firstChunk + "data: {\"error\":{\"code\":503,\"status\":\"UNAVAILABLE\",\"message\":\"overloaded\"}}\n\n" + firstChunk
	r := runExec(t, execOpts{streaming: true, up: func(n int, req *http.Request, _ string) (*http.Response, error) {
		return okResp(newCtxBody(req.Context(), "", nil, inband)), nil
	}})
	if !r.ok || r.attempts != 1 {
		t.Fatalf("ok=%v attempts=%d", r.ok, r.attempts)
	}
	assertTerminalError(t, r.body)
	if strings.Count(r.body, "first words") != 1 {
		t.Fatalf("a chunk after the error must not be processed: %s", r.body)
	}
}
