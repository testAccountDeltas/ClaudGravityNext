// Part of the tlsfingerprint package's port from superrelay (v1)'s
// tlsfingerprint.go — see roundtripper.go for the split rationale and
// the frozen-behavior constraint.
package tlsfingerprint

import (
	"context"
	"sync"

	"golang.org/x/net/http2"
)

func (t *fingerprintedRoundTripper) h2ConnFor(ctx context.Context, host, addr string) (*http2.ClientConn, error) {
	t.mu.Lock()
	for {
		if conn, ok := t.h2Conns[addr]; ok {
			if conn.CanTakeNewRequest() {
				t.mu.Unlock()
				return conn, nil
			}
			delete(t.h2Conns, addr)
		}
		cond, inFlight := t.dialing[addr]
		if !inFlight {
			break
		}
		cond.Wait()
	}

	cond := sync.NewCond(&t.mu)
	t.dialing[addr] = cond
	t.mu.Unlock()

	// dialH2 panicking (not just returning an error) used to skip this
	// cleanup entirely — the stale t.dialing[addr] entry never gets
	// removed and cond.Broadcast() never fires, so every OTHER goroutine
	// already blocked in cond.Wait() above, and every future call for
	// this same addr, waits forever: one panic here permanently wedges
	// every request to that upstream host, not just the one that panicked.
	var conn *http2.ClientConn
	var err error
	defer func() {
		t.mu.Lock()
		delete(t.dialing, addr)
		cond.Broadcast()
		if err == nil && conn != nil {
			t.h2Conns[addr] = conn
		}
		t.mu.Unlock()
	}()

	conn, err = t.dialH2(ctx, host, addr)
	return conn, err
}

// dialH2 establishes a fingerprinted TLS connection and, when ALPN selected
// HTTP/2, wraps it as an HTTP/2 client connection. A nil connection with a
// nil error means the peer chose HTTP/1.1.
func (t *fingerprintedRoundTripper) dialH2(ctx context.Context, host, addr string) (*http2.ClientConn, error) {
	tlsConn, err := t.dialTLS(ctx, host, addr)
	if err != nil {
		return nil, err
	}
	if tlsConn.ConnectionState().NegotiatedProtocol != http2.NextProtoTLS {
		_ = tlsConn.Close()
		return nil, nil
	}
	h2Transport := &http2.Transport{
		ReadIdleTimeout: h2ReadIdleTimeout,
		PingTimeout:     h2PingTimeout,
		IdleConnTimeout: h2IdleConnTimeout,
	}
	h2Conn, err := h2Transport.NewClientConn(tlsConn)
	if err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	return h2Conn, nil
}
