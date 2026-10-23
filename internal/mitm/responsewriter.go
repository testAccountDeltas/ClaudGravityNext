// Part of the mitm package's port from superrelay (v1)'s mitm.go — see
// ca.go for the split rationale and the frozen-behavior constraint.
package mitm

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// mitmIdleDeadline bounds how long an intercepted connection can go
// completely silent before it's torn down — refreshed on every read/write
// instead of set once, so it only fires on genuine inactivity. A single
// absolute SetDeadline here used to kill the connection 30 real wall-clock
// seconds after the handshake no matter what, which is well inside a real
// generation's TTFT (this gateway's own panic/TTFT watchdogs allow up to
// 180s) — any client routed through this intercepted path (HTTPS_PROXY set,
// no ANTHROPIC_BASE_URL) doing real work past 30s got its response torn off
// mid-stream.
const mitmIdleDeadline = 10 * time.Minute

type MitmResponseWriter struct {
	mu          sync.Mutex
	conn        net.Conn
	header      http.Header
	buf         bytes.Buffer
	statusCode  int
	wroteHeader bool
	headersSent bool
	chunked     bool
	sentOffset  int
	// writeErr is set the moment a real socket write to the client fails
	// (client disconnected). A Round 15 audit caught this had no equivalent
	// at all — Write() kept buffering a handler's output into memory for
	// the rest of a generation nobody could still read, with nothing
	// upstream ever told the connection was already dead.
	writeErr error
}

func newMitmResponseWriter(conn net.Conn) *MitmResponseWriter {
	return &MitmResponseWriter{
		conn:       conn,
		header:     make(http.Header),
		statusCode: http.StatusOK,
	}
}

func (w *MitmResponseWriter) Header() http.Header {
	return w.header
}

func (w *MitmResponseWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// A Round 15 audit caught this never surfaced a prior socket-write
	// failure — the handler (and the upstream-read loop feeding it, via
	// SSEStreamer.SendRaw) had no way to learn the client was already
	// gone and kept buffering/reading from upstream for nothing. Returning
	// the stored error here lets io.Copy-style callers and SendRaw's own
	// error check stop immediately instead of discovering it only once
	// the whole response is done.
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.buf.Write(b)
}

func (w *MitmResponseWriter) WriteHeader(statusCode int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.wroteHeader {
		w.statusCode = statusCode
		w.wroteHeader = true
	}
}

// sendHeaders writes the status line and headers exactly once. chunked is
// true for a real mid-stream Flush (SSE), where the final body size isn't
// known yet; false for Close's single-shot path, where the handler has
// already returned and the whole body is sitting in w.buf, so a precise
// Content-Length can still be set just like before this type supported
// incremental streaming at all.
func (w *MitmResponseWriter) sendHeaders(chunked bool) {
	w.headersSent = true
	w.chunked = chunked
	if chunked {
		w.header.Set("Transfer-Encoding", "chunked")
		// An audit caught that a Content-Length header copied from the
		// original upstream response (before this handler decided to
		// stream the body chunked instead) could still be sitting in
		// w.header here — RFC 7230 §3.3.2 forbids sending both
		// Transfer-Encoding and Content-Length on the same response, and
		// strict HTTP/1.1 parsers (Node.js/Bun, which Claude Code's CLI
		// runs on) reject the whole response over it.
		w.header.Del("Content-Length")
	} else if w.statusCode == http.StatusNoContent {
		// An audit caught this unconditionally set Content-Length (to 0,
		// since a 204 body is always empty) — RFC 7230 §3.3.2 says a
		// server MUST NOT send Content-Length on a 204 response at all,
		// not even a zero one; some strict HTTP/1.1 parsers (Node.js/Bun,
		// which Claude Code's own CLI runs on) reject it outright.
		w.header.Del("Content-Length")
	} else if w.header.Get("Content-Length") == "" {
		w.header.Set("Content-Length", strconv.Itoa(w.buf.Len()))
	}
	w.header.Set("Connection", "close")

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("HTTP/1.1 %d %s\r\n", w.statusCode, http.StatusText(w.statusCode)))
	for k, vv := range w.header {
		for _, v := range vv {
			sb.WriteString(fmt.Sprintf("%s: %s\r\n", k, v))
		}
	}
	sb.WriteString("\r\n")
	_ = w.conn.SetWriteDeadline(time.Now().Add(mitmIdleDeadline))
	// An audit caught that the original tlsConn.SetDeadline call before
	// this handler started (serveInterceptedTLS) sets BOTH read and write
	// deadlines to the same absolute instant, but only the write side ever
	// gets refreshed (here and in writeNewBytes below) — the disconnect-
	// watcher goroutine's own tlsConn.Read is still bound by that original,
	// never-renewed READ deadline, so a genuinely long-but-active
	// generation gets its connection killed exactly mitmIdleDeadline after
	// it STARTED, not after it went idle. Refresh the read side on every
	// chunk too, the same idle-timeout pattern this file already uses
	// correctly elsewhere (serveTunneledConnection's src/dst deadline
	// refresh) — this keeps a real upper bound against a leaked goroutine
	// on a silently-dead connection, just one that resets on genuine
	// activity instead of the connection's start time.
	_ = w.conn.SetReadDeadline(time.Now().Add(mitmIdleDeadline))
	_, _ = w.conn.Write([]byte(sb.String()))
}

// writeNewBytes pushes only what's been written to w.buf since the last
// call — not the whole buffer — as either a raw slice (fixed
// Content-Length framing) or one chunked-encoding frame. Previously Flush()
// replayed the status line, every header, and the ENTIRE buffer
// accumulated so far on every single call, so any stream with more than
// one chunk (every real SSE response) sent the client a response with its
// headers and earlier chunks duplicated over and over.
func (w *MitmResponseWriter) writeNewBytes() {
	all := w.buf.Bytes()
	if w.sentOffset >= len(all) {
		return
	}
	chunk := all[w.sentOffset:]
	_ = w.conn.SetWriteDeadline(time.Now().Add(mitmIdleDeadline))
	// Same read-deadline refresh as sendHeaders above, and for the same
	// reason — this is the per-chunk activity point for a real SSE stream.
	_ = w.conn.SetReadDeadline(time.Now().Add(mitmIdleDeadline))
	var writeErr error
	if w.chunked {
		if _, writeErr = w.conn.Write([]byte(fmt.Sprintf("%x\r\n", len(chunk)))); writeErr == nil {
			if _, writeErr = w.conn.Write(chunk); writeErr == nil {
				_, writeErr = w.conn.Write([]byte("\r\n"))
			}
		}
	} else {
		_, writeErr = w.conn.Write(chunk)
	}
	// A Round 15 audit caught two gaps here:
	// (1) w.buf never shrinks — only sentOffset advances — so a long SSE
	// stream (a multi-thousand-token reasoning turn) keeps the ENTIRE
	// response accumulated in memory for the whole connection's lifetime.
	// Reset the buffer once its already-sent prefix has actually left the
	// wire, instead of growing it forever.
	// (2) a write error was silently discarded — Write/WriteHeader calls
	// from the handler above kept right on buffering into w.buf for the
	// rest of a generation the client already disconnected from, with
	// nothing upstream ever noticing the connection was dead.
	if writeErr != nil {
		w.writeErr = writeErr
		return
	}
	w.buf.Reset()
	w.sentOffset = 0
}

// Flush is called mid-handler by anything holding this writer as an
// http.Flusher (SSEStreamer.SendRaw, in particular) — exactly the case
// sendHeaders(true)/writeNewBytes above exist to make safe.
func (w *MitmResponseWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.headersSent {
		w.sendHeaders(true)
	}
	w.writeNewBytes()
}

// Close finalizes the response after the handler has returned. If Flush
// was never called mid-handler (every single-shot JSON handler — oauth/
// usage, bootstrap, models, non-streaming /v1/messages), this is the ONLY
// flush point and the full body is already buffered, so it's sent once
// with an exact Content-Length — unchanged from the old behavior. If Flush
// WAS already called (a real SSE stream), headers and earlier chunks are
// already on the wire in chunked framing; this just sends the trailing
// bytes and the terminating zero-length chunk.
func (w *MitmResponseWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.headersSent {
		w.sendHeaders(false)
	}
	w.writeNewBytes()
	if w.chunked {
		_ = w.conn.SetWriteDeadline(time.Now().Add(mitmIdleDeadline))
		_, _ = w.conn.Write([]byte("0\r\n\r\n"))
	}
}
