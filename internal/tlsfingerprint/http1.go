// Part of the tlsfingerprint package's port from superrelay (v1)'s
// tlsfingerprint.go — see roundtripper.go for the split rationale and
// the frozen-behavior constraint.
package tlsfingerprint

import (
	"bufio"
	"io"
	"net"
	"net/http"
)

func (t *fingerprintedRoundTripper) roundTripHTTP1(req *http.Request, host, addr string) (*http.Response, error) {
	tlsConn, err := t.dialTLS(req.Context(), host, addr)
	if err != nil {
		return nil, err
	}
	if err = req.Write(tlsConn); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(tlsConn), req)
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	resp.Body = &fingerprintConnClosingBody{ReadCloser: resp.Body, conn: tlsConn}
	return resp, nil
}

// fingerprintConnClosingBody ties a response body to the connection that
// produced it, since this pool-less HTTP/1.1 path has nothing else closing it.
type fingerprintConnClosingBody struct {
	io.ReadCloser
	conn net.Conn
}

func (b *fingerprintConnClosingBody) Close() error {
	err := b.ReadCloser.Close()
	if errConn := b.conn.Close(); err == nil {
		err = errConn
	}
	return err
}
