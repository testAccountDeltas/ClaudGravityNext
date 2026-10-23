// Part of the mitm package's port from superrelay (v1)'s mitm.go — see
// ca.go for the split rationale and the frozen-behavior constraint.
package mitm

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"time"

	"claudgravity/internal/logx"
)

// idleCopy is io.Copy with a true idle timeout: the deadline is pushed
// forward before every read, so it only fires after idleDeadline of
// genuine silence, never on total connection lifetime.
func idleCopy(dst net.Conn, src net.Conn, idleDeadline time.Duration) {
	buf := make([]byte, 32*1024)
	for {
		_ = src.SetReadDeadline(time.Now().Add(idleDeadline))
		n, err := src.Read(buf)
		if n > 0 {
			_ = dst.SetWriteDeadline(time.Now().Add(idleDeadline))
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func serveInterceptedTLS(tlsConn *tls.Conn, handler http.Handler) {
	defer tlsConn.Close()
	_ = tlsConn.SetDeadline(time.Now().Add(mitmIdleDeadline))

	defer func() {
		if rec := recover(); rec != nil {
			logx.LogGateway("[SuperRelay Gateway] MITM handler panic (recovered): %v", rec)
		}
	}()

	if err := tlsConn.Handshake(); err != nil {
		logx.LogGateway("[SuperRelay Gateway] MITM TLS handshake failed: %v", err)
		return
	}
	_ = tlsConn.SetDeadline(time.Now().Add(mitmIdleDeadline))

	reader := bufio.NewReader(tlsConn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		logx.LogGateway("[SuperRelay Gateway] MITM request parse failed: %v", err)
		return
	}

	// http.ReadRequest builds req with context.Background() — real, since
	// this path parses raw requests off a hijacked CONNECT tunnel instead of
	// going through http.Server (which would wire req.Context() to cancel
	// on client disconnect automatically). Without this, a client hitting
	// Ctrl+C mid-generation while routed through HTTPS_PROXY (rather than
	// the direct :8900 TLS API) never propagated cancellation to the
	// upstream Google call at all — r.Context() downstream was real but
	// never actually tied to this connection closing.
	// Drain the whole body into memory BEFORE starting the disconnect
	// watcher below. req.Body is a lazy reader pulling from the same
	// `reader` (bufio.Reader) wrapping tlsConn — whatever body bytes
	// weren't already sitting in its buffer from the header read still
	// have to come from a real tlsConn.Read() the first time something
	// consumes req.Body. The watcher goroutine's own tlsConn.Read() raced
	// directly against that on the raw connection, bypassing `reader`
	// entirely, so it could steal a body byte meant for the request parser
	// — silently corrupting any POST body that didn't fit in bufio's
	// initial read (JSON parse failures in handleMessages, confirmed as
	// the actual failure mode for bodies over a few KB).
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		logx.LogGateway("[SuperRelay Gateway] MITM read body failed: %v", err)
		return
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Only safe to read tlsConn directly now that the body is fully
		// drained above — nothing else will touch `reader`/tlsConn for
		// input again on this one-shot connection (see the comment below).
		// Any read activity here — new bytes or an error/EOF — means the
		// client is done with this tunnel either way, so stop the upstream
		// call.
		// An audit caught that this only read ONCE: if the client sent
		// unexpected bytes (n>0, err==nil — a stray pipelined byte, a
		// benign protocol quirk, anything short of an actual close/EOF),
		// the comment's own assumption ("new bytes... means the client
		// is done") was wrong, but worse, the goroutine then exited
		// without ever calling cancel() — permanently ending this
		// connection's disconnect detection. A GENUINE later disconnect
		// (the user hitting Ctrl+C) then went completely unnoticed,
		// leaving the upstream Gemini call running to completion for no
		// reason. Loop on an unexpected read instead of exiting.
		buf := make([]byte, 1)
		for {
			n, err := tlsConn.Read(buf)
			if err != nil || n == 0 {
				cancel()
				return
			}
		}
	}()
	req = req.WithContext(ctx)

	rw := newMitmResponseWriter(tlsConn)
	handler.ServeHTTP(rw, req)
	rw.Close()
	// Always close after one request-response pair instead of looping for
	// keep-alive reuse: these intercepted calls (oauth/usage, bootstrap) are
	// low-frequency control calls, and a hand-rolled HTTP/1.1 writer sharing
	// one TLS connection across multiple pipelined requests is a needless
	// source of framing bugs that silently kill the tunnel with no log line.
}
