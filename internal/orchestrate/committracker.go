// Part of the orchestrate package — see pipeline.go for the package doc
// comment. commitTracker records whether ANY byte (status line, headers or
// body) has gone out through the client's ResponseWriter, across every
// retry attempt of one request. Deferred SSE commit means an attempt that
// fails before its first content block leaves the response untouched and
// the next attempt can still start fresh — but a keepalive ping (needed to
// hold a slow connection open) does commit the 200, and from then on a
// failure can only be reported as an SSE error event, never a new HTTP
// status. Execute consults Committed() for exactly that decision.
package orchestrate

import (
	"net/http"
	"sync/atomic"
)

type commitTracker struct {
	http.ResponseWriter
	committed atomic.Bool
}

func (c *commitTracker) WriteHeader(code int) {
	c.committed.Store(true)
	c.ResponseWriter.WriteHeader(code)
}

func (c *commitTracker) Write(b []byte) (int, error) {
	c.committed.Store(true)
	return c.ResponseWriter.Write(b)
}

// Committed reports whether the response has been (partially) written.
func (c *commitTracker) Committed() bool { return c.committed.Load() }

// Unwrap lets http.ResponseController reach the real writer.
func (c *commitTracker) Unwrap() http.ResponseWriter { return c.ResponseWriter }

// flushCommitTracker adds Flush, only when the wrapped writer has one — so
// "is this writer a Flusher" (which selects streaming vs non-streaming)
// keeps its original answer after wrapping.
type flushCommitTracker struct{ *commitTracker }

func (f flushCommitTracker) Flush() {
	f.ResponseWriter.(http.Flusher).Flush()
}

type committer interface{ Committed() bool }

// trackCommit wraps w unless it is already tracked.
func trackCommit(w http.ResponseWriter) (http.ResponseWriter, committer) {
	if c, ok := w.(committer); ok {
		return w, c
	}
	ct := &commitTracker{ResponseWriter: w}
	if _, ok := w.(http.Flusher); ok {
		return flushCommitTracker{ct}, ct
	}
	return ct, ct
}
