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
	"strings"
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

func isWebSocketUpgrade(req *http.Request) bool {
	if !strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, v := range req.Header.Values("Connection") {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
				return true
			}
		}
	}
	return false
}

func serveInterceptedTLS(tlsConn *tls.Conn, handler http.Handler) {
	closeConn := true
	defer func() {
		if closeConn {
			_ = tlsConn.Close()
		}
	}()
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

	// Drain the whole body into memory BEFORE starting the disconnect
	// watcher below so watcher reads never steal buffered POST body bytes.
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		logx.LogGateway("[SuperRelay Gateway] MITM read body failed: %v", err)
		return
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(bodyBytes))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !isWebSocketUpgrade(req) {
		go func() {
			buf := make([]byte, 1)
			for {
				n, err := tlsConn.Read(buf)
				if err != nil || n == 0 {
					cancel()
					return
				}
			}
		}()
	}
	req = req.WithContext(ctx)

	rw := newMitmResponseWriterWithReader(tlsConn, reader)
	handler.ServeHTTP(rw, req)
	if rw.Hijacked() {
		closeConn = false
		return
	}
	rw.Close()
	// Always close after one request-response pair instead of looping for
	// keep-alive reuse: these intercepted calls (oauth/usage, bootstrap) are
	// low-frequency control calls, and a hand-rolled HTTP/1.1 writer sharing
	// one TLS connection across multiple pipelined requests is a needless
	// source of framing bugs that silently kill the tunnel with no log line.
}
