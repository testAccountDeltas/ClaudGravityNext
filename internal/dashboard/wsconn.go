// Part of the dashboard package's PARTIAL port from superrelay (v1)'s
// ws_hub.go + ui.go.
//
// PORTED (this file + wshub.go): the entire WSHub/WSClient connection
// hub — registration, broadcast, heartbeat, safe-send/close, the
// WebSocket connection handler itself. This is pure infrastructure with
// no dependency on anything account-shaped.
//
// NOT YET PORTED: all of ui.go (RegisterUIHandlers and every
// handleAPI*/handleOAuth* function). Read: 9 of ui.go's 15 functions
// call internal/accounts' *AccountManager directly (GetAccounts,
// DeleteAccount, ToggleAccountDisabled, RefreshAllQuotas,
// WarmupSingleAccount, WarmupAllFreshAccounts, BuildGoogleAuthURL,
// ExchangeAuthCode, the model catalog), which doesn't exist yet — Phase
// 2. The remaining 6 (requireDashboardAuth, handleAPIBootstrap,
// handleAPIKeys, handleDashboard, handleAPISessions,
// handleAPISessionDetail) COULD be ported standalone today, but
// RegisterUIHandlers — the one function that actually wires any of them
// into a working dashboard — needs all 15 together. Porting 6 of 15
// handlers with no way to register them yet would be partial-port
// theater, not progress; deferring the whole file to Phase 2, when
// internal/accounts exists and the real wiring can be written once, is
// the honest call here (same reasoning as warmup.go's
// AccountManager-dependent methods in this same migration phase).
package dashboard

import (
	"encoding/json"
	"time"

	"golang.org/x/net/websocket"
)

// wsConn is the subset of golang.org/x/net/websocket.Conn's (which
// satisfies net.Conn) methods WSClient actually uses — letting tests
// inject a fake connection instead of needing a real WebSocket handshake.
type wsConn interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
	SetWriteDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
}

// HandleWS upgrades HTTP connection to WebSocket and registers client to hub.
func HandleWS(ws *websocket.Conn) {
	client := &WSClient{
		ws:   ws,
		send: make(chan []byte, 128),
	}
	globalWSHub.register <- client

	// Writer pump: pipes send channel to WebSocket
	go func() {
		defer client.requestUnregister(globalWSHub)
		for msg := range client.send {
			_ = ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := ws.Write(msg); err != nil {
				return
			}
		}
	}()

	// Reader pump: reads control messages / client pings
	defer client.requestUnregister(globalWSHub)

	buf := make([]byte, 1024)
	for {
		_ = ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := ws.Read(buf)
		if err != nil {
			break
		}
		if n > 0 {
			var incoming struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(buf[:n], &incoming); err == nil {
				if incoming.Type == "pong" {
					// Client alive acknowledgment
				}
			}
		}
	}
}
