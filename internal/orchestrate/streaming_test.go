package orchestrate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"claudgravity/internal/config"
	"claudgravity/internal/reqctx"
)

// roundTripFunc wraps a function as an http.RoundTripper.
type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestStreaming_ContextCanceled_ReportsStatus499(t *testing.T) {
	origTransport := httpClient.Transport
	defer func() { httpClient.Transport = origTransport }()

	// Server streams chunks forever until context cancellation
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	httpClient.Transport = http.DefaultTransport

	ctx, cancel := context.WithCancel(context.Background())

	p := &reqctx.Params{
		RID:           101,
		IsStreaming:   true,
		IsAntigravity: true,
		ServerCfg: config.ServerConfig{
			PanicTimeoutSeconds:      60,
			TTFTTimeoutSeconds:       60,
			MaxStreamDurationSeconds: 60,
		},
		RawClaudeBody: []byte(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}]}`),
	}
	st := &reqctx.State{
		UpstreamURL: server.URL,
		StartedAt:   time.Now(),
	}

	rec := httptest.NewRecorder()

	// Cancel context shortly after streaming begins
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	ok, status, err := runStreamingAttempt(ctx, p, st, "test-acc", []byte(`{}`), rec, rec)
	if !ok {
		t.Fatalf("expected ok=true for streaming attempt that began streaming, got ok=%v", ok)
	}
	if status != 499 {
		t.Fatalf("expected status=499 (Client Closed Request) on context cancellation, got %d (err: %v)", status, err)
	}
}

func TestStreaming_PanicTicker_DoesNotBlockFailoverWhenAntigravity(t *testing.T) {
	origTransport := httpClient.Transport
	defer func() { httpClient.Transport = origTransport }()

	// Simulate slow upstream that returns 429 after 1.2 seconds
	var reqReceived sync.WaitGroup
	reqReceived.Add(1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqReceived.Done()
		time.Sleep(1200 * time.Millisecond)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`))
	}))
	defer server.Close()

	httpClient.Transport = http.DefaultTransport

	p := &reqctx.Params{
		RID:           102,
		IsStreaming:   true,
		IsAntigravity: true, // canFailover is true
		ServerCfg: config.ServerConfig{
			PanicTimeoutSeconds:      1, // fires at 1s before upstream responds at 1.2s
			TTFTTimeoutSeconds:       10,
			MaxStreamDurationSeconds: 60,
		},
		RawClaudeBody: []byte(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}]}`),
	}
	st := &reqctx.State{
		Attempt:     0,
		UpstreamURL: server.URL,
		StartedAt:   time.Now(),
	}

	rec := httptest.NewRecorder()
	ok, status, err := runStreamingAttempt(context.Background(), p, st, "test-acc", []byte(`{}`), rec, rec)

	// Since canFailover is true, panicTicker must NOT have committed headers early.
	// Therefore runStreamingAttempt must return ok=false allowing pipeline.go to fail over.
	if ok {
		t.Fatalf("expected ok=false so caller can failover to next account/host, got ok=true")
	}
	if status != http.StatusTooManyRequests {
		t.Fatalf("expected status=429, got %d (err: %v)", status, err)
	}
	// Verify that response recorder headers were not committed
	if rec.Code != http.StatusOK && rec.Code != 0 && rec.Body.Len() > 0 {
		t.Fatalf("expected no body written to client ResponseWriter, got %q", rec.Body.String())
	}
	_ = fmt.Sprint(err)
}

type trackingReadCloser struct {
	io.Reader
	closed bool
	mu     sync.Mutex
}

func (t *trackingReadCloser) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closed = true
	return nil
}

func (t *trackingReadCloser) isClosed() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.closed
}

func TestStreaming_WatchdogTimeout_ResponseBodyClosed(t *testing.T) {
	origTransport := httpClient.Transport
	defer func() { httpClient.Transport = origTransport }()

	tracker := &trackingReadCloser{Reader: strings.NewReader("some response body")}

	httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		// Wait until watchdog context is canceled
		<-req.Context().Done()
		time.Sleep(20 * time.Millisecond)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       tracker,
			Header:     make(http.Header),
		}, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &reqctx.Params{
		RID:           103,
		IsStreaming:   true,
		IsAntigravity: true,
		ServerCfg: config.ServerConfig{
			PanicTimeoutSeconds:      60,
			TTFTTimeoutSeconds:       1, // watchdog times out after 1s
			MaxStreamDurationSeconds: 60,
		},
		RawClaudeBody: []byte(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}]}`),
	}
	st := &reqctx.State{
		Attempt:     0,
		UpstreamURL: "http://example.com",
		StartedAt:   time.Now(),
	}

	rec := httptest.NewRecorder()
	ok, _, _ := runStreamingAttempt(ctx, p, st, "test-acc", []byte(`{}`), rec, rec)
	if ok {
		t.Fatalf("expected ok=false on watchdog timeout, got true")
	}

	// Verify that the body was closed by the cleanup goroutine
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tracker.isClosed() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !tracker.isClosed() {
		t.Fatalf("expected response body to be closed on watchdog timeout")
	}
}
