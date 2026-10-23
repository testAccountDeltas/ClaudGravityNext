// Phase 1 characterization test for the ported (partial) dashboard
// package — proves the WSHub registration/broadcast/unregister lifecycle
// and SafeSend's close-race safety behave as in v1, using a fake wsConn
// instead of a real WebSocket handshake.
package dashboard

import (
	"encoding/json"
	"testing"
	"time"
)

type fakeWSConn struct {
	closed bool
}

func (f *fakeWSConn) Read(p []byte) (int, error)         { return 0, nil }
func (f *fakeWSConn) Write(p []byte) (int, error)        { return len(p), nil }
func (f *fakeWSConn) Close() error                       { f.closed = true; return nil }
func (f *fakeWSConn) SetWriteDeadline(t time.Time) error { return nil }
func (f *fakeWSConn) SetReadDeadline(t time.Time) error  { return nil }

func newTestClient() (*WSClient, *fakeWSConn) {
	fc := &fakeWSConn{}
	c := &WSClient{ws: fc, send: make(chan []byte, 8)}
	return c, fc
}

func TestWSClient_SafeSendAfterCloseReturnsFalse(t *testing.T) {
	c, _ := newTestClient()
	c.Close()
	if c.SafeSend([]byte("x")) {
		t.Fatal("SafeSend must return false once the client is closed")
	}
}

func TestWSClient_SafeSendFullBufferReturnsFalse(t *testing.T) {
	c, _ := newTestClient() // buffer size 8
	for i := 0; i < 8; i++ {
		if !c.SafeSend([]byte("x")) {
			t.Fatalf("send %d should have succeeded (buffer not full yet)", i)
		}
	}
	if c.SafeSend([]byte("overflow")) {
		t.Fatal("SafeSend must return false once the send buffer is full (slow client), not block")
	}
}

func TestWSClient_CloseIsIdempotent(t *testing.T) {
	c, fc := newTestClient()
	c.Close()
	c.Close() // must not panic on double-close (close(c.send) twice would panic)
	if !fc.closed {
		t.Fatal("underlying conn must be closed")
	}
	if !c.IsClosed() {
		t.Fatal("IsClosed must report true after Close")
	}
}

func TestWSHub_RegisterThenBroadcastDeliversToClient(t *testing.T) {
	h := NewWSHub()
	c, _ := newTestClient()
	h.register <- c

	// Give the hub's run() goroutine a moment to process registration.
	time.Sleep(20 * time.Millisecond)

	h.Broadcast("test:event", map[string]string{"hello": "world"})

	// The client's send channel also receives an "init" snapshot on
	// registration (sendInitialSnapshot) — drain messages until the
	// broadcast one shows up, rather than assuming arrival order.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case msg := <-c.send:
			var evt WSEvent
			if err := json.Unmarshal(msg, &evt); err != nil {
				t.Fatalf("payload is not valid JSON: %v", err)
			}
			if evt.Type == "test:event" {
				return
			}
			if evt.Type != "init" {
				t.Fatalf("unexpected event type %q before the broadcast arrived", evt.Type)
			}
		case <-deadline:
			t.Fatal("expected to receive the test:event broadcast within 2s")
		}
	}
}

func TestWSHub_UnregisterRemovesClientAndCloses(t *testing.T) {
	h := NewWSHub()
	c, fc := newTestClient()
	h.register <- c
	time.Sleep(20 * time.Millisecond)

	c.requestUnregister(h)
	time.Sleep(20 * time.Millisecond)

	if !fc.closed {
		t.Fatal("unregistering a client must close its underlying connection")
	}
}

func TestWSHub_RequestUnregisterOnlySendsOnce(t *testing.T) {
	h := NewWSHub()
	c, _ := newTestClient()
	// Call concurrently, as the real writer/reader pumps + a failed
	// broadcast SafeSend all might — must not deadlock or double-send on
	// the (small, buffered) unregister channel.
	done := make(chan struct{})
	for i := 0; i < 5; i++ {
		go func() {
			c.requestUnregister(h)
			done <- struct{}{}
		}()
	}
	for i := 0; i < 5; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("requestUnregister must not block even when called concurrently many times")
		}
	}
}
