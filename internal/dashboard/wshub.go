// Package dashboard is a PARTIAL port of superrelay (v1)'s ws_hub.go
// (this file + wsconn.go) and ui.go. See wsconn.go's doc comment for
// what's ported now vs. deferred, and why.
//
// BroadcastAccounts and sendInitialSnapshot's accounts/catalog fields
// were explicitly left out when this package was first ported (Phase 1)
// because internal/accounts didn't exist yet. Now that it does (Phase
// 2), both are completed here — this is the documented follow-through
// from that deferral, not new scope. internal/dashboard importing
// internal/accounts is the correct direction (the dashboard displays
// accounts, accounts knows nothing about the dashboard — see
// internal/accounts' own Broadcaster interface for the inverse
// direction, which is the one that would have been wrong).
package dashboard

import (
	"encoding/json"
	"sync"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/requestlog"
)

// WSEvent represents a standard JSON message pushed to WebSocket clients.
type WSEvent struct {
	Type      string      `json:"type"`
	Timestamp int64       `json:"timestamp"`
	Data      interface{} `json:"data,omitempty"`
}

// WSClient wraps a single WebSocket connection.
type WSClient struct {
	ws             wsConn
	send           chan []byte
	closed         bool
	mu             sync.Mutex
	unregisterOnce sync.Once
}

// WSHub coordinates live connections and broadcasts events to all connected clients.
type WSHub struct {
	clients    map[*WSClient]bool
	register   chan *WSClient
	unregister chan *WSClient
	broadcast  chan []byte
	mu         sync.RWMutex
}

var globalWSHub = NewWSHub()

func NewWSHub() *WSHub {
	h := &WSHub{
		clients:    make(map[*WSClient]bool),
		register:   make(chan *WSClient, 32),
		unregister: make(chan *WSClient, 32),
		broadcast:  make(chan []byte, 256),
	}
	go h.run()
	return h
}

func (h *WSHub) run() {
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case client := <-h.register:
			if client.IsClosed() {
				continue
			}
			h.mu.Lock()
			h.clients[client] = true
			h.mu.Unlock()
			// Send initial state snapshot immediately to the new client
			go h.sendInitialSnapshot(client)

		case client := <-h.unregister:
			h.mu.Lock()
			delete(h.clients, client)
			h.mu.Unlock()
			client.Close()

		case msg := <-h.broadcast:
			h.mu.RLock()
			for client := range h.clients {
				if !client.SafeSend(msg) {
					// Closed or slow client buffer full: unregister to avoid stalling
					go client.requestUnregister(h)
				}
			}
			h.mu.RUnlock()

		case <-heartbeat.C:
			// Ping active clients to keep connection alive through proxies
			pingEvent, _ := json.Marshal(WSEvent{
				Type:      "ping",
				Timestamp: time.Now().UnixMilli(),
			})
			h.mu.RLock()
			for client := range h.clients {
				client.SafeSend(pingEvent)
			}
			h.mu.RUnlock()
		}
	}
}

// requestUnregister sends this client to the hub's unregister channel
// exactly once, however many of (writer pump, reader pump, a failed
// broadcast SafeSend) notice the disconnect — each used to send
// independently on a small (32-slot) buffered channel with a plain
// blocking send, so a burst of simultaneous client disconnects (e.g. a
// server restart dropping every open dashboard tab's WebSocket at once)
// could file up to 2-3x as many unregister sends as there are clients,
// filling that buffer and leaving the extra goroutines blocked until the
// hub's single-threaded run() loop drained far more entries than there
// were ever real clients.
func (c *WSClient) requestUnregister(h *WSHub) {
	c.unregisterOnce.Do(func() {
		h.unregister <- c
	})
}

func (c *WSClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.send)
		_ = c.ws.Close()
	}
}

func (c *WSClient) IsClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// SafeSend queues payload on the client's send channel, holding the same
// mutex Close() uses so a send can never race a close(c.send) and panic.
// Returns false if the client is already closed or its buffer is full
// (slow client) — callers treat both as "drop/unregister", never as a
// reason to send to the channel directly.
func (c *WSClient) SafeSend(payload []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	select {
	case c.send <- payload:
		return true
	default:
		return false
	}
}

func (h *WSHub) Broadcast(eventType string, data interface{}) {
	payload, err := json.Marshal(WSEvent{
		Type:      eventType,
		Timestamp: time.Now().UnixMilli(),
		Data:      data,
	})
	if err != nil {
		return
	}

	select {
	case h.broadcast <- payload:
	default:
		// Broadcast channel full: drop to prevent gateway stalls
	}
}

func (h *WSHub) BroadcastLog(reqLog *requestlog.RequestLog) {
	if reqLog == nil {
		return
	}
	h.Broadcast("log:entry", reqLog)
}

// BroadcastAccounts completes the deferral from Phase 1 — see this
// file's package doc comment. Its only caller for now is
// internal/accounts itself, via the Broadcaster interface it defines
// (wired up at the composition root, once that exists, by calling
// accounts.SetBroadcaster(wsHub)); ui.go, v1's other caller, is still
// deferred.
func (h *WSHub) BroadcastAccounts(accountList []*accounts.Record) {
	h.Broadcast("accounts:update", accountList)
}

func (h *WSHub) BroadcastAccountDeleted(email string) {
	h.Broadcast("account:deleted", map[string]string{"email": email})
}

func (h *WSHub) BroadcastSessionsUpdate() {
	sessions := requestlog.QuerySessions(50)
	h.Broadcast("sessions:update", sessions)
}

// sendInitialSnapshot now matches v1 in full — the accounts/catalog
// fields deferred in Phase 1 are completed here (see this file's
// package doc comment).
func (h *WSHub) sendInitialSnapshot(client *WSClient) {
	accountList := accounts.AllAccounts()
	sessions := requestlog.QuerySessions(50)
	logs, stats := requestlog.Query("", "", "", "", false, 100)
	catalog := accounts.ModelCatalog().GetAll()

	snapshot := map[string]interface{}{
		"accounts": accountList,
		"sessions": sessions,
		"logs":     logs,
		"stats":    stats,
		"catalog":  catalog,
	}

	payload, err := json.Marshal(WSEvent{
		Type:      "init",
		Timestamp: time.Now().UnixMilli(),
		Data:      snapshot,
	})
	if err == nil {
		client.SafeSend(payload)
	}
}
