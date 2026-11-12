// Part of the tlsfingerprint package's port from superrelay (v1)'s
// tlsfingerprint.go — see roundtripper.go for the split rationale and
// the frozen-behavior constraint.
package tlsfingerprint

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http/httpproxy"
)

type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) {
	return b.r.Read(p)
}

func (t *fingerprintedRoundTripper) dialTLS(ctx context.Context, host, addr string) (*utls.UConn, error) {
	dialer := &net.Dialer{Timeout: fingerprintDialTimeout, KeepAlive: 30 * time.Second}

	targetURL := &url.URL{Scheme: "https", Host: addr}
	proxyFunc := httpproxy.FromEnvironment().ProxyFunc()
	proxyURL, err := proxyFunc(targetURL)
	if err != nil {
		return nil, fmt.Errorf("resolve proxy: %w", err)
	}

	var rawConn net.Conn
	if proxyURL != nil {
		proxyAddr := proxyURL.Host
		if proxyURL.Port() == "" {
			if proxyURL.Scheme == "https" {
				proxyAddr = net.JoinHostPort(proxyURL.Hostname(), "443")
			} else {
				proxyAddr = net.JoinHostPort(proxyURL.Hostname(), "80")
			}
		}
		rawConn, err = dialer.DialContext(ctx, "tcp", proxyAddr)
		if err != nil {
			return nil, fmt.Errorf("dial proxy %s: %w", proxyAddr, err)
		}

		connectReq := &http.Request{
			Method: "CONNECT",
			URL:    &url.URL{Opaque: addr},
			Host:   addr,
			Header: make(http.Header),
		}
		if proxyURL.User != nil {
			u := proxyURL.User.Username()
			p, _ := proxyURL.User.Password()
			auth := base64.StdEncoding.EncodeToString([]byte(u + ":" + p))
			connectReq.Header.Set("Proxy-Authorization", "Basic "+auth)
		}

		if err := connectReq.Write(rawConn); err != nil {
			_ = rawConn.Close()
			return nil, fmt.Errorf("write CONNECT to proxy: %w", err)
		}

		br := bufio.NewReader(rawConn)
		resp, err := http.ReadResponse(br, connectReq)
		if err != nil {
			_ = rawConn.Close()
			return nil, fmt.Errorf("read CONNECT response from proxy: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = rawConn.Close()
			return nil, fmt.Errorf("proxy CONNECT rejected with status %d: %s", resp.StatusCode, resp.Status)
		}

		if br.Buffered() > 0 {
			rawConn = &bufferedConn{
				Conn: rawConn,
				r:    io.MultiReader(io.LimitReader(br, int64(br.Buffered())), rawConn),
			}
		}
	} else {
		rawConn, err = dialer.DialContext(ctx, directNetwork, addr)
		if err != nil {
			return nil, err
		}
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = rawConn.SetDeadline(deadline)
	} else {
		_ = rawConn.SetDeadline(time.Now().Add(fingerprintDialTimeout))
	}

	tlsConn := utls.UClient(rawConn, &utls.Config{
		ServerName: host,
		RootCAs:    t.rootCAs,
	}, t.helloID)
	if err = tlsConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	_ = rawConn.SetDeadline(time.Time{}) // clear: streaming responses must not inherit the handshake deadline
	return tlsConn, nil
}
