package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"claudgravity/internal/requestlog"
)

// dialWS opens a real dashboard WebSocket (same-origin, Basic auth).
func dialWS(t *testing.T, e *adminEnv) *websocket.Conn {
	t.Helper()
	cfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws", e.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Header = http.Header{}
	cfg.Header.Set("Authorization", "Basic "+basicAuth("admin", e.pass))
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

func basicAuth(u, p string) string {
	req, _ := http.NewRequest("GET", "http://x", nil)
	req.SetBasicAuth(u, p)
	return strings.TrimPrefix(req.Header.Get("Authorization"), "Basic ")
}

// nextEvent reads frames until one of the wanted type arrives.
func nextEvent(t *testing.T, ws *websocket.Conn, want string) WSEvent {
	t.Helper()
	for {
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		var raw []byte
		if err := websocket.Message.Receive(ws, &raw); err != nil {
			t.Fatalf("waiting for %q: %v", want, err)
		}
		var ev WSEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			t.Fatalf("bad frame %q: %v", raw, err)
		}
		if ev.Type == want {
			return ev
		}
	}
}

func TestWebSocket_SnapshotThenLiveEvents(t *testing.T) {
	e := newAdminEnv(t)
	ws := dialWS(t, e)

	init := nextEvent(t, ws, "init")
	data, _ := init.Data.(map[string]interface{})
	for _, k := range []string{"accounts", "sessions", "logs", "stats", "catalog"} {
		if _, ok := data[k]; !ok {
			t.Errorf("snapshot lacks %q", k)
		}
	}

	globalWSHub.BroadcastLog(nil) // a nil entry must be ignored, not crash the hub
	globalWSHub.BroadcastLog(&requestlog.RequestLog{Account: "a@example.com", Status: 200})
	ev := nextEvent(t, ws, "log:entry")
	if m, _ := ev.Data.(map[string]interface{}); m["account"] != "a@example.com" {
		t.Fatalf("log entry payload: %v", ev.Data)
	}

	globalWSHub.BroadcastAccountDeleted("gone@example.com")
	if ev = nextEvent(t, ws, "account:deleted"); !strings.Contains(toJSON(ev.Data), "gone@example.com") {
		t.Fatalf("deleted payload: %v", ev.Data)
	}
	globalWSHub.BroadcastSessionsUpdate()
	nextEvent(t, ws, "sessions:update")
}

func toJSON(v interface{}) string { b, _ := json.Marshal(v); return string(b) }

func TestWebSocket_ClientHangUpUnregistersIt(t *testing.T) {
	e := newAdminEnv(t)
	count := func() int {
		globalWSHub.mu.RLock()
		defer globalWSHub.mu.RUnlock()
		return len(globalWSHub.clients)
	}
	before := count()
	ws := dialWS(t, e)
	nextEvent(t, ws, "init")
	if count() != before+1 {
		t.Fatalf("clients = %d, want %d", count(), before+1)
	}
	_ = websocket.Message.Send(ws, `{"type":"pong"}`) // client acknowledgement is accepted silently
	_ = ws.Close()
	for end := time.Now().Add(5 * time.Second); count() != before; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("closed client still registered (%d, want %d)", count(), before)
		}
		globalWSHub.Broadcast("nudge", nil) // a failed send is what notices a dead peer
	}
}

func TestWebSocket_ARejectedHandshakeDoesNotRegisterAnything(t *testing.T) {
	e := newAdminEnv(t)
	cfg, _ := websocket.NewConfig("ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws", "https://evil.example")
	cfg.Header = http.Header{}
	cfg.Header.Set("Authorization", "Basic "+basicAuth("admin", e.pass))
	if _, err := websocket.DialConfig(cfg); err == nil {
		t.Fatal("a cross-site handshake must be refused")
	}
}

func TestRegisterUIHandlers_RootServesTheDashboardAndAPIsPassThrough(t *testing.T) {
	e := newAdminEnv(t)
	mux := http.NewServeMux()
	RegisterUIHandlers(mux, e.cm)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	get := func(path string, hdr map[string]string, auth bool) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		if auth {
			req.SetBasicAuth("admin", e.pass)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if st := get("/", nil, false); st != http.StatusUnauthorized {
		t.Fatalf("the dashboard root needs the password: %d", st)
	}
	if st := get("/", nil, true); st != http.StatusOK {
		t.Fatalf("dashboard root with credentials: %d", st)
	}
}
