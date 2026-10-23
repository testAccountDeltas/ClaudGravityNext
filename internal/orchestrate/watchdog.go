// Part of the orchestrate package — see pipeline.go for the package doc
// comment. idleWatchdog is superrelay (v1)'s executeStreamingWithWatchdog
// idle/panic-timeout machinery extracted into its own reusable piece,
// per the migration spec's Part 4.3 file list (watchdog.go: "panic/TTFT
// timeout as a reusable piece, not inlined").
package orchestrate

import (
	"context"
	"sync/atomic"
	"time"

	"claudgravity/internal/logx"
)

// StreamingChunkTimeout is the inter-chunk timeout during streaming (120-180s)
// to accommodate large tool calls (Write/Edit) where Gemini buffers long JSON
// arguments before emitting chunks.
const StreamingChunkTimeout = 180 * time.Second

// idleWatchdog cancels its context when upstream goes silent for longer
// than the active idle timeout (ttftTimeout before any bytes are received,
// chunkTimeout once streaming starts), with maxDuration as an absolute safety
// net against a connection that trickles just enough to never go idle. Idle-activity
// timeout instead of a flat total-duration ceiling: a huge Write call can
// legitimately take minutes once Gemini actually starts answering
// (confirmed live: 20K+ output tokens buffered as one lump, ~120s), and a
// fixed "whole request must finish in N minutes" deadline can't tell that
// apart from a genuinely hung connection.
type idleWatchdog struct {
	ctx    context.Context
	cancel context.CancelFunc

	lastActivity atomic.Int64
	firstTouch   atomic.Bool
	done         chan struct{}
}

// startIdleWatchdog derives ctx from baseCtx (the original http.Request's
// context) instead of context.Background() — a client that disconnects
// (Ctrl+C, closed terminal, dropped connection) now actually cancels the
// in-flight upstream Google request immediately (net/http cancels a
// request's Context on client disconnect) instead of letting the
// generation run to completion anyway and silently burning the account's
// quota for a response nobody will ever read.
func startIdleWatchdog(baseCtx context.Context, rid uint64, ttftTimeout, chunkTimeout, maxDuration time.Duration) *idleWatchdog {
	if ttftTimeout <= 0 {
		ttftTimeout = 180 * time.Second
	}
	if chunkTimeout <= 0 {
		chunkTimeout = StreamingChunkTimeout
	}
	if maxDuration <= 0 {
		maxDuration = 30 * time.Minute
	}
	ctx, cancel := context.WithCancel(baseCtx)
	w := &idleWatchdog{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	w.lastActivity.Store(time.Now().UnixNano())

	go func() {
		defer close(w.done)
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		start := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				limit := ttftTimeout
				phase := "TTFT"
				if w.firstTouch.Load() {
					limit = chunkTimeout
					phase = "inter-chunk"
				}
				silentFor := time.Since(time.Unix(0, w.lastActivity.Load()))
				if silentFor > limit {
					logx.LogGateway("[#%d] !! Idle watchdog (%s): no bytes from upstream for %.0fs (limit %.0fs) -> cancelling", rid, phase, silentFor.Seconds(), limit.Seconds())
					cancel()
					return
				}
				if time.Since(start) > maxDuration {
					logx.LogGateway("[#%d] !! Absolute stream-duration safety net hit (%.0fs) -> cancelling", rid, maxDuration.Seconds())
					cancel()
					return
				}
			}
		}
	}()
	return w
}

// Touch records upstream activity, transitioning the watchdog from the
// initial TTFT phase to the inter-chunk streaming phase and resetting the idle clock.
func (w *idleWatchdog) Touch() {
	w.firstTouch.Store(true)
	w.lastActivity.Store(time.Now().UnixNano())
}

// Stop cancels the watchdog and waits for its goroutine to exit. MUST be
// called (deferred) before the caller returns: the watchdog goroutine
// only exits via ctx.Done() (or its own timeout firing) — a real deadlock
// of exactly this kind shipped in v1 when this ordering was violated: the
// response had already been fully written and displayed client-side, but
// the handler never returned, so Go's http.Server never sent the final
// chunk/closed the connection, and the client saw an unterminated stream
// after already showing the complete, correct reply.
func (w *idleWatchdog) Stop() {
	w.cancel()
	<-w.done
}
