package orchestrate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"claudgravity/internal/config"
	"claudgravity/internal/reqctx"
)

// attemptEnv drives ONE runStreamingAttempt against a scripted transport, with
// the panic (keepalive) and first-byte timeouts the real server would use.
type attemptEnv struct {
	antigravity bool
	panicSec    int
	ttftSec     int
	ctx         context.Context
	up          func(req *http.Request) (*http.Response, error)
}

type attemptRes struct {
	ok     bool
	status int
	err    error
	body   string
	code   int
}

func runAttempt(t *testing.T, e attemptEnv) attemptRes {
	t.Helper()
	// The upstream call runs in a goroutine the attempt may abandon (timeout,
	// client gone). Wait for the transport function to return before the
	// cleanup swaps the transport back, so the two never overlap.
	var inflight sync.WaitGroup
	started := make(chan struct{}, 1)
	restore := SetTransportForTest(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		inflight.Add(1)
		defer inflight.Done()
		select {
		case started <- struct{}{}:
		default:
		}
		return e.up(req)
	}))
	t.Cleanup(func() {
		select {
		case <-started:
			inflight.Wait()
		case <-time.After(3 * time.Second):
		}
		restore()
	})
	if e.ctx == nil {
		e.ctx = context.Background()
	}
	p := &reqctx.Params{RID: 970, IsStreaming: true, IsAntigravity: e.antigravity,
		ServerCfg:     config.ServerConfig{PanicTimeoutSeconds: e.panicSec, TTFTTimeoutSeconds: e.ttftSec, MaxStreamDurationSeconds: 30},
		RawClaudeBody: []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)}
	st := &reqctx.State{UpstreamURL: "http://upstream.invalid/x", StartedAt: time.Now()}
	rec := httptest.NewRecorder()
	var r attemptRes
	r.ok, r.status, r.err = runStreamingAttempt(e.ctx, p, st, "acc", []byte(`{}`), rec, rec)
	r.body, r.code = rec.Body.String(), rec.Code
	return r
}

func goodSSE() string { return tfSSE(tfWrap(tfGood)) }

// slowThen answers after d, unless the request is cancelled first.
func slowThen(d time.Duration, resp func() (*http.Response, error)) func(*http.Request) (*http.Response, error) {
	return func(req *http.Request) (*http.Response, error) {
		select {
		case <-time.After(d):
			return resp()
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
}

func TestAttempt_DirectKey_SlowUpstreamGetsAnEarlyPingThenTheAnswer(t *testing.T) {
	r := runAttempt(t, attemptEnv{panicSec: 1, ttftSec: 10,
		up: slowThen(1500*time.Millisecond, func() (*http.Response, error) { return respBody(200, goodSSE()), nil })})
	if !r.ok || r.status != 200 || r.code != 200 {
		t.Fatalf("ok=%v status=%d code=%d err=%v", r.ok, r.status, r.code, r.err)
	}
	// Repeat pings are throttled by stream.minPingGap (covered in the stream
	// package); at this speed exactly the one early ping is expected.
	if n := strings.Count(r.body, "event: ping"); n != 1 {
		t.Fatalf("a silent upstream must be answered with one early ping, got %d:\n%s", n, r.body)
	}
	if !strings.HasPrefix(r.body, "event: ping") {
		t.Fatalf("the first byte on the wire must be the early ping:\n%.200s", r.body)
	}
	if !strings.Contains(r.body, "hello from a healthy turn") || !strings.Contains(r.body, "message_stop") {
		t.Fatalf("the real answer must follow the pings:\n%s", r.body)
	}
}

func TestAttempt_DirectKey_ErrorAfterEarlyPingBecomesATerminalSSEError(t *testing.T) {
	r := runAttempt(t, attemptEnv{panicSec: 1, ttftSec: 10,
		up: slowThen(1300*time.Millisecond, func() (*http.Response, error) {
			return respBody(429, `{"error":{"status":"RESOURCE_EXHAUSTED"}}`), nil
		})})
	if !r.ok || r.status != 429 {
		t.Fatalf("headers were already committed: ok must be true (nothing else may be written), status=%d ok=%v", r.status, r.ok)
	}
	if !strings.Contains(r.body, "event: error") || !strings.Contains(r.body, "rate_limit_error") || !strings.Contains(r.body, "Upstream HTTP 429") {
		t.Fatalf("want a terminal rate_limit_error event:\n%s", r.body)
	}
	if strings.Contains(r.body, "message_stop") {
		t.Fatal("an error stream must not end like a successful one")
	}
}

func TestAttempt_DirectKey_TransportFailureAfterEarlyPing(t *testing.T) {
	boom := errors.New("connection reset by peer")
	r := runAttempt(t, attemptEnv{panicSec: 1, ttftSec: 10,
		up: slowThen(1300*time.Millisecond, func() (*http.Response, error) { return nil, boom })})
	if !r.ok || r.status != http.StatusBadGateway {
		t.Fatalf("ok=%v status=%d", r.ok, r.status)
	}
	if !strings.Contains(r.body, "overloaded_error") || !strings.Contains(r.body, "connection reset by peer") {
		t.Fatalf("want overloaded_error carrying the cause:\n%s", r.body)
	}
}

func TestAttempt_DirectKey_NoAnswerAtAllEndsWithATimeoutErrorEvent(t *testing.T) {
	r := runAttempt(t, attemptEnv{panicSec: 1, ttftSec: 2,
		up: func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}})
	if !r.ok || r.status != http.StatusGatewayTimeout {
		t.Fatalf("ok=%v status=%d err=%v", r.ok, r.status, r.err)
	}
	if !strings.Contains(r.body, "Upstream timed out: no response received") || !strings.Contains(r.body, "event: ping") {
		t.Fatalf("want pings followed by a timeout error event:\n%s", r.body)
	}
}

func TestAttempt_Antigravity_SlowUpstreamNeverSendsEarlyHeaders(t *testing.T) {
	r := runAttempt(t, attemptEnv{antigravity: true, panicSec: 1, ttftSec: 10,
		up: slowThen(1500*time.Millisecond, func() (*http.Response, error) { return respBody(200, goodSSE()), nil })})
	if !r.ok || r.status != 200 {
		t.Fatalf("ok=%v status=%d err=%v", r.ok, r.status, r.err)
	}
	if strings.Contains(r.body, "event: ping") {
		t.Fatalf("failover must stay possible, so no early ping may be sent:\n%s", r.body)
	}
	if !strings.Contains(r.body, "hello from a healthy turn") {
		t.Fatalf("answer missing:\n%s", r.body)
	}
}

func TestAttempt_Antigravity_NoAnswerBeforeTheTimeoutLeavesTheResponseUntouched(t *testing.T) {
	r := runAttempt(t, attemptEnv{antigravity: true, panicSec: 1, ttftSec: 2,
		up: func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}})
	if r.ok || r.status != http.StatusGatewayTimeout || r.body != "" {
		t.Fatalf("a timeout with nothing sent must be retryable by the caller: ok=%v status=%d body=%q", r.ok, r.status, r.body)
	}
}

func TestAttempt_ClientHangsUpWhileWaiting_Reports499(t *testing.T) {
	for _, c := range []struct {
		name     string
		panicSec int
		cancelAt time.Duration
		honour   bool // does the transport notice the cancellation?
	}{
		{"before the keepalive window, transport returns the cancellation", 30, 150 * time.Millisecond, true},
		{"inside the wait loop, transport returns the cancellation", 1, 1400 * time.Millisecond, true},
		{"inside the wait loop, transport ignores the cancellation", 1, 1400 * time.Millisecond, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			time.AfterFunc(c.cancelAt, cancel)
			r := runAttempt(t, attemptEnv{antigravity: true, panicSec: c.panicSec, ttftSec: 30, ctx: ctx,
				up: func(req *http.Request) (*http.Response, error) {
					if c.honour {
						<-req.Context().Done()
						return nil, req.Context().Err()
					}
					time.Sleep(3 * time.Second)
					return nil, errors.New("late")
				}})
			if r.ok || r.status != 499 || r.body != "" {
				t.Fatalf("a client that left is 499 with nothing written: ok=%v status=%d body=%q err=%v", r.ok, r.status, r.body, r.err)
			}
		})
	}
}
