package stream

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"claudgravity/internal/reqctx"
)

// syncRecorder is a concurrency-safe ResponseWriter+Flusher that signals
// every ping frame on a channel (the ping goroutine writes concurrently
// with the reader goroutine).
type syncRecorder struct {
	mu    sync.Mutex
	buf   strings.Builder
	hdr   http.Header
	pings chan struct{}
}

func newSyncRecorder() *syncRecorder {
	return &syncRecorder{hdr: http.Header{}, pings: make(chan struct{}, 1024)}
}
func (r *syncRecorder) Header() http.Header { return r.hdr }
func (r *syncRecorder) WriteHeader(int)     {}
func (r *syncRecorder) Flush()              {}
func (r *syncRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	r.buf.Write(b)
	r.mu.Unlock()
	if strings.Contains(string(b), "event: ping") {
		select {
		case r.pings <- struct{}{}:
		default:
		}
	}
	return len(b), nil
}
func (r *syncRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

func fastPings(t *testing.T) {
	t.Helper()
	oi, og := keepaliveInterval, minPingGap
	keepaliveInterval, minPingGap = time.Millisecond, 0
	t.Cleanup(func() { keepaliveInterval, minPingGap = oi, og })
}

func waitPing(t *testing.T, r *syncRecorder) {
	t.Helper()
	select {
	case <-r.pings:
	case <-time.After(5 * time.Second):
		t.Fatal("no keepalive ping while upstream was quiet")
	}
}

func assertNothingAfter(t *testing.T, body, terminal string) {
	t.Helper()
	i := strings.LastIndex(body, terminal)
	if i < 0 {
		t.Fatalf("missing %s: %s", terminal, body)
	}
	if rest := body[i:]; strings.Contains(rest[len(terminal):], "event: ") {
		t.Fatalf("event written after %s: %q", terminal, rest)
	}
}

func TestKeepalive_PingsWhileQuiet_NeverAfterMessageStop(t *testing.T) {
	fastPings(t)
	rec := newSyncRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, &reqctx.Params{ModelName: "claude-opus-4-5"}, &reqctx.State{})
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- StreamGeminiToClaude(f, pr, nil) }()

	_, _ = pw.Write([]byte("data: " + string(chunk(`{"text":"hi"}`, "")) + "\n\n"))
	waitPing(t, rec) // quiet upstream -> pings keep flowing
	waitPing(t, rec)
	_, _ = pw.Write([]byte("data: " + string(chunk(``, "STOP")) + "\n\n"))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	assertNothingAfter(t, rec.body(), "event: message_stop")
	before := rec.body()
	if err := w.SendKeepalivePing(); err != nil || rec.body() != before {
		t.Fatalf("a ping after message_stop must be a no-op (err=%v)", err)
	}
}

func TestKeepalive_NeverAfterErrorEvent(t *testing.T) {
	fastPings(t)
	rec := newSyncRecorder()
	w := NewSafeSSEWriter(1, rec, rec, nil)
	f := NewFSM(w, 1, &reqctx.Params{ModelName: "claude-opus-4-5"}, &reqctx.State{})
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- StreamGeminiToClaude(f, pr, nil) }()

	_, _ = pw.Write([]byte("data: " + string(chunk(`{"text":"hi"}`, "")) + "\n\n"))
	waitPing(t, rec)
	pw.CloseWithError(io.ErrUnexpectedEOF) // connection dropped mid-stream
	err := <-done
	if err == nil {
		t.Fatal("a broken read must surface as an error")
	}
	f.FinishWithError(err)
	body := rec.body()
	if !strings.Contains(body, "event: error") || strings.Contains(body, "message_stop") {
		t.Fatalf("want a terminal error without message_stop: %s", body)
	}
	assertNothingAfter(t, body, "event: error")
	before := rec.body()
	_ = w.SendKeepalivePing()
	if rec.body() != before {
		t.Fatal("ping after the error event")
	}
}
