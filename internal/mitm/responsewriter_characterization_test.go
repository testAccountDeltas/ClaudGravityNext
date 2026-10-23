// Phase 1 characterization test for the ported mitm package — proves
// MitmResponseWriter's documented Round-15 fixes still hold: Flush never
// re-sends already-sent bytes, a write error is stored and returned by
// later Write calls, 204 responses never carry Content-Length, and a
// chunked (streamed) response never also carries Content-Length.
package mitm

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// fakeConn is a minimal net.Conn backed by an in-memory buffer, enough for
// MitmResponseWriter's Write/SetDeadline calls without a real socket.
type fakeConn struct {
	net.Conn
	written strings.Builder
	failing bool
}

func (f *fakeConn) Write(b []byte) (int, error) {
	if f.failing {
		return 0, net.ErrClosed
	}
	return f.written.Write(b)
}
func (f *fakeConn) SetWriteDeadline(time.Time) error { return nil }
func (f *fakeConn) SetReadDeadline(time.Time) error  { return nil }

func TestMitmResponseWriter_FlushDoesNotResendEarlierChunks(t *testing.T) {
	conn := &fakeConn{}
	w := newMitmResponseWriter(conn)

	_, _ = w.Write([]byte("chunk1"))
	w.Flush()
	firstLen := conn.written.Len()

	_, _ = w.Write([]byte("chunk2"))
	w.Flush()

	out := conn.written.String()
	if strings.Count(out, "chunk1") != 1 {
		t.Fatalf("chunk1 must appear exactly once across both Flush calls, got: %q", out)
	}
	if conn.written.Len() <= firstLen && len(out) > 0 {
		// Second flush must have written something new (chunk2's frame).
		t.Fatalf("second Flush must send new bytes, total length did not grow: %d -> %d", firstLen, conn.written.Len())
	}
}

func TestMitmResponseWriter_WriteErrorIsStickyAndReturned(t *testing.T) {
	conn := &fakeConn{}
	w := newMitmResponseWriter(conn)
	_, _ = w.Write([]byte("chunk1"))
	conn.failing = true
	w.Flush() // the socket write inside writeNewBytes fails here

	_, err := w.Write([]byte("chunk2"))
	if err == nil {
		t.Fatal("once a socket write has failed, subsequent Write calls must return that error instead of silently buffering forever")
	}
}

func TestMitmResponseWriter_NoContentNeverSetsContentLength(t *testing.T) {
	conn := &fakeConn{}
	w := newMitmResponseWriter(conn)
	w.WriteHeader(http.StatusNoContent)
	w.Close()

	if strings.Contains(strings.ToLower(conn.written.String()), "content-length") {
		t.Fatalf("a 204 response must never carry Content-Length (RFC 7230 §3.3.2), got: %q", conn.written.String())
	}
}

func TestMitmResponseWriter_ChunkedNeverSetsContentLength(t *testing.T) {
	conn := &fakeConn{}
	w := newMitmResponseWriter(conn)
	w.Header().Set("Content-Length", "999") // stale, as if copied from an upstream response
	_, _ = w.Write([]byte("partial"))
	w.Flush() // forces chunked framing (headers not sent yet, Flush -> sendHeaders(true))

	head := conn.written.String()
	if strings.Contains(strings.ToLower(head), "content-length") {
		t.Fatalf("a chunked response must never also carry Content-Length (RFC 7230 §3.3.2), got headers: %q", head)
	}
	if !strings.Contains(head, "Transfer-Encoding: chunked") {
		t.Fatalf("expected Transfer-Encoding: chunked once Flush is called before Close, got: %q", head)
	}
}

func TestMitmResponseWriter_WriteHeaderOnlyTakesFirstCall(t *testing.T) {
	conn := &fakeConn{}
	w := newMitmResponseWriter(conn)
	w.WriteHeader(http.StatusCreated)
	w.WriteHeader(http.StatusInternalServerError) // must be ignored
	w.Close()

	if !strings.Contains(conn.written.String(), "201 Created") {
		t.Fatalf("the FIRST WriteHeader call must win, got: %q", conn.written.String())
	}
}
