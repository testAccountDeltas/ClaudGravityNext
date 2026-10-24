// Part of the stream package — see events.go for the package doc
// comment. SafeSSEWriter is NEW code (no single v1 equivalent — v1's
// SendRaw/SendKeepalivePing, now events.go's formatting half, had no
// headers-sent/finished bookkeeping of their own at all), built directly
// against the real GAP-D1-01 incident: v1's handleMessages wrote a second,
// conflicting HTTP error response on top of a streaming response that had
// already committed its headers and possibly several SSE events, because
// nothing in the codebase had a single source of truth for "have headers
// already gone out through this response."
package stream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// EventRecorder is the optional capture hook for every SSE event/ping
// this writer emits — internal/speccapture's own methods satisfy this
// without stream needing to import that package directly (same
// dependency-inversion pattern as internal/requestlog/internal/accounts'
// Broadcaster in earlier phases). A nil recorder is a silent no-op.
type EventRecorder interface {
	AppendClaudeEvent(rid uint64, event, data string)
}

// SafeSSEWriter is the one place a byte of this request's SSE response
// actually reaches the client, and the one place that tracks whether
// that's happened yet.
type SafeSSEWriter struct {
	rid      uint64
	w        http.ResponseWriter
	flusher  http.Flusher
	recorder EventRecorder

	mu            sync.Mutex
	headersSent   bool
	finished      bool
	lastEventTime time.Time

	// commitHook, when set, runs once immediately before the first byte
	// this writer puts on the wire (event or ping) — the owner uses it to
	// write the HTTP 200 + SSE headers lazily, so a request that fails
	// before producing anything never commits a status line.
	commitHook func()
	hookDone   bool

	// holding buffers every non-ping event (message_start, thinking, ...)
	// until Release: nothing of the turn reaches the client before the
	// first visible content (or a clean finish), so a bad turn can still be
	// discarded and retried on another account.
	holding   bool
	held      []heldEvent
	heldBytes int
}

type heldEvent struct{ event, data string }

// maxHeldBytes force-releases a hold that grew past this size (an enormous
// thinking block) so buffering can never become unbounded.
const maxHeldBytes = 512 * 1024

// NewSafeSSEWriter constructs a writer for one request's SSE response.
// recorder may be nil.
func NewSafeSSEWriter(rid uint64, w http.ResponseWriter, flusher http.Flusher, recorder EventRecorder) *SafeSSEWriter {
	return &SafeSSEWriter{
		rid:           rid,
		w:             w,
		flusher:       flusher,
		recorder:      recorder,
		lastEventTime: time.Now(),
	}
}

// commitHeadersLocked marks that this writer has (or is about to) put
// bytes on the wire — called under s.mu, immediately before the first
// actual write. Once true, the only safe way to report a later failure is
// another SSE event, never a fresh HTTP status line.
func (s *SafeSSEWriter) commitHeadersLocked() {
	s.headersSent = true
}

// EmitEvent writes one SSE event, a no-op once Finish/TerminateWithError
// has run.
func (s *SafeSSEWriter) EmitEvent(event string, data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return nil
	}
	if s.holding && event != "ping" {
		s.held = append(s.held, heldEvent{event, data})
		s.heldBytes += len(data)
		if s.heldBytes > maxHeldBytes {
			return s.releaseLocked()
		}
		return nil
	}
	return s.writeEventLocked(event, data)
}

func (s *SafeSSEWriter) writeEventLocked(event, data string) error {
	s.commitLocked()
	s.lastEventTime = time.Now()
	if s.recorder != nil {
		s.recorder.AppendClaudeEvent(s.rid, event, data)
	}
	return writeAndFlush(s.w, s.flusher, formatSSEEvent(event, data))
}

// commitLocked runs the lazy-commit hook (once) and marks headers sent.
func (s *SafeSSEWriter) commitLocked() {
	if s.commitHook != nil && !s.hookDone {
		s.hookDone = true
		s.commitHook()
	}
	s.commitHeadersLocked()
}

// SetCommitHook registers the function that writes the HTTP status line and
// headers; it runs once, right before this writer's first byte.
func (s *SafeSSEWriter) SetCommitHook(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commitHook = fn
}

// Commit forces the commit hook now (early-headers keepalive path).
func (s *SafeSSEWriter) Commit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commitLocked()
}

// HoldEvents starts buffering every non-ping event until Release.
func (s *SafeSSEWriter) HoldEvents() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holding = true
}

// Release flushes the buffered events in order and stops buffering. A
// no-op when not holding.
func (s *SafeSSEWriter) Release() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.releaseLocked()
}

func (s *SafeSSEWriter) releaseLocked() error {
	if !s.holding {
		return nil
	}
	s.holding = false
	held := s.held
	s.held, s.heldBytes = nil, 0
	if s.finished {
		return nil
	}
	for _, e := range held {
		if err := s.writeEventLocked(e.event, e.data); err != nil {
			return err
		}
	}
	return nil
}

// DiscardHeld drops the buffered events and finishes the writer — the
// attempt failed before any content reached the client.
func (s *SafeSSEWriter) DiscardHeld() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.held, s.heldBytes = nil, 0
	s.finished = true
}

// IsReleased reports whether buffered events (if any were ever held) have
// reached the client, i.e. the turn can no longer be silently retried.
func (s *SafeSSEWriter) IsReleased() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.holding
}

// minPingGap is the real silence required before a ping is written; a
// package var only so tests can shrink it.
var minPingGap = 4 * time.Second

// SendKeepalivePing writes a ping frame, throttled to at most once every
// 4 seconds of real silence — matches v1's own SendKeepalivePing.
func (s *SafeSSEWriter) SendKeepalivePing() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished || time.Since(s.lastEventTime) < minPingGap {
		return nil
	}
	// GAP-S1: a ping genuinely puts bytes on the wire — commitHeadersLocked
	// must fire here too, or a later TerminateWithError (e.g. the upstream
	// call erroring out right after only pings, no real SSE event, went
	// out) sees headersSent still false, writes a fresh HTTP error status
	// on top of a connection that already has ping bytes on it, and panics
	// with "superfluous response.WriteHeader" — the exact GAP-D1-01 shape
	// this writer exists to prevent, just reached through the ping path
	// instead of EmitEvent.
	s.commitLocked()
	s.lastEventTime = time.Now()
	return writeAndFlush(s.w, s.flusher, formatSSEKeepalivePing())
}

// Finish marks this writer done; every later EmitEvent/SendKeepalivePing
// becomes a no-op rather than panicking on a second message_stop or
// writing past a connection the caller has already considered closed.
func (s *SafeSSEWriter) Finish() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finished = true
}

// HeadersSent reports whether this writer has put any byte on the wire
// yet.
func (s *SafeSSEWriter) HeadersSent() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.headersSent
}

// TerminateWithError is the single decision point GAP-D1-01 needed: if
// headers already went out through this writer, the only safe thing left
// to do is emit an event:error SSE frame — attempting to also write a
// real HTTP status now would panic with "superfluous
// response.WriteHeader" and corrupt the open connection for any client
// SDK mid-stream-parsing it. Reports that case back as sentAsSSE=true.
// If headers haven't gone out yet, this writer does nothing — there is
// still a clean HTTP response available — and the caller (internal/
// httpapi, once built) is responsible for writing the real status/body;
// v1's GAP-D1-01 incident was exactly this branch missing:
// handleMessages's post-retry-loop error write didn't check "did headers
// already go out," and double-wrote on top of an already-committed
// streaming response.
func (s *SafeSSEWriter) TerminateWithError(errType, message string) (sentAsSSE bool) {
	// GAP-S2: the actual write used to happen AFTER releasing s.mu, unlike
	// every other write path on this type (EmitEvent holds the lock across
	// its own writeAndFlush via defer) — a concurrent EmitEvent/ping could
	// interleave its own write with this one on the same socket. Holding
	// the lock for the whole call, same as EmitEvent, also means the
	// EventRecorder call below (previously skipped entirely on this path)
	// can't race a concurrent recorder call either.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return s.headersSent
	}
	wasHeadersSent := s.headersSent
	s.finished = true

	if !wasHeadersSent {
		return false
	}
	// GAP-D1-10: message can contain arbitrary upstream-sourced bytes —
	// %q escapes non-ASCII/control chars as \xHH, illegal in JSON
	// (RFC 8259). errType is always this package's own fixed identifier
	// string, safe as %q.
	msgBytes, _ := json.Marshal(message)
	errPayload := fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%s}}`, errType, msgBytes)
	if s.recorder != nil {
		s.recorder.AppendClaudeEvent(s.rid, "error", errPayload)
	}
	_ = writeAndFlush(s.w, s.flusher, formatSSEEvent("error", errPayload))
	return true
}

// MarkHeadersAlreadySent records that headers for this request already
// went out through a path OTHER than this writer (e.g. orchestrate's own
// commitHeaders() calling w.WriteHeader(200) directly, immediately before
// constructing this writer) — GAP-S1: without this, a brand-new writer's
// headersSent starts false regardless of what the real socket already
// has on it, so a TerminateWithError called before this writer's own
// first EmitEvent/ping would wrongly report "headers not sent" and tell
// the caller it's safe to write a fresh HTTP status, which panics with
// "superfluous response.WriteHeader". Every current call site happens to
// call EmitEvent immediately after constructing the writer in that
// situation (which self-heals this via commitHeadersLocked), but that's
// fragile — a future call site that doesn't would silently reintroduce
// the exact GAP-D1-01 incident this type exists to prevent.
func (s *SafeSSEWriter) MarkHeadersAlreadySent() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headersSent = true
}
