package artifacts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"claudgravity/internal/logx"
	"golang.org/x/net/websocket"
)

// subscriptionToken uses only RFC 6455 token characters ([0-9a-f_]) because
// claude.exe sends it as the second Sec-WebSocket-Protocol subprotocol.
func mintSubscriptionToken(secret []byte, slug string, now time.Time) (token string, exp int64) {
	exp = now.Add(liveTokenTTL).Unix()
	expHex := strconv.FormatInt(exp, 16)
	mac := computeTokenMAC(secret, slug, expHex)
	return "fl1_" + expHex + "_" + mac, exp
}

func verifySubscriptionToken(secret []byte, slug, token string, now time.Time) bool {
	parts := strings.Split(token, "_")
	if len(parts) != 3 || parts[0] != "fl1" {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 16, 64)
	if err != nil || exp < now.Unix() {
		return false
	}
	expected := computeTokenMAC(secret, slug, parts[1])
	return hmac.Equal([]byte(parts[2]), []byte(expected))
}

func computeTokenMAC(secret []byte, slug, expHex string) string {
	m := hmac.New(sha256.New, secret)
	_, _ = m.Write([]byte(slug))
	_, _ = m.Write([]byte("|"))
	_, _ = m.Write([]byte(expHex))
	return hex.EncodeToString(m.Sum(nil)[:24])
}

func (h *Handler) mintLiveToken(slug string) (token string, exp int64, err error) {
	sec, err := h.store.LiveSecret()
	if err != nil {
		return "", 0, err
	}
	tok, exp := mintSubscriptionToken(sec, slug, time.Now())
	return tok, exp, nil
}

func (h *Handler) attachSubscriptionToken(slug string, resp map[string]any) {
	if tok, exp, err := h.mintLiveToken(slug); err == nil {
		resp["subscriptionToken"] = tok
		resp["subscriptionTokenExp"] = exp
		resp["watchTokenRenewEnabled"] = true
	}
}

func (h *Handler) watchToken(w http.ResponseWriter, r *http.Request, slug string) {
	if !slugRe.MatchString(slug) {
		writeErr(w, http.StatusNotFound, "not_found_error", "artifact not found")
		return
	}
	a, err := h.store.Get(slug)
	if err != nil {
		h.storeErr(w, err)
		return
	}
	tok, exp, err := h.mintLiveToken(slug)
	if err != nil {
		h.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ver":                  a.VersionString(),
		"subscriptionToken":    tok,
		"subscriptionTokenExp": exp,
	})
}

func offeredWebSocketProtocols(r *http.Request) []string {
	var out []string
	for _, hdr := range r.Header.Values("Sec-Websocket-Protocol") {
		for _, item := range strings.Split(hdr, ",") {
			if p := strings.TrimSpace(item); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

func (h *Handler) authorizeLiveWS(r *http.Request, slug string) bool {
	sec, err := h.store.LiveSecret()
	if err != nil {
		return false
	}
	now := time.Now()
	hasProto := false
	hasToken := false
	for _, p := range offeredWebSocketProtocols(r) {
		if p == liveSubprotocol {
			hasProto = true
			continue
		}
		if verifySubscriptionToken(sec, slug, p, now) {
			hasToken = true
		}
	}
	return hasProto && hasToken
}

func (h *Handler) serveLiveWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/edge-api/frame-live/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "ws" || !slugRe.MatchString(parts[0]) {
		writeErr(w, http.StatusNotFound, "not_found_error", "Not found: "+r.Method+" "+r.URL.Path)
		return
	}
	slug := parts[0]
	if _, err := h.store.Get(slug); err != nil {
		h.storeErr(w, err)
		return
	}
	if !h.authorizeLiveWS(r, slug) {
		writeErr(w, http.StatusUnauthorized, "authentication_error", "invalid or missing live subscription token")
		return
	}
	wsSrv := websocket.Server{
		Handshake: func(cfg *websocket.Config, _ *http.Request) error {
			cfg.Protocol = []string{liveSubprotocol}
			return nil
		},
		Handler: func(ws *websocket.Conn) {
			h.runLiveWSConn(slug, ws)
		},
	}
	wsSrv.ServeHTTP(w, r)
}

func (h *Handler) runLiveWSConn(slug string, ws *websocket.Conn) {
	defer ws.Close()
	cli := &wsClient{
		send: make(chan string, wsSendQueueCap),
		done: make(chan struct{}),
	}
	h.live.registerWS(slug, cli)

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case msg := <-cli.send:
				_ = ws.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
				if err := websocket.Message.Send(ws, msg); err != nil {
					_ = ws.Close()
					return
				}
			case <-cli.done:
				return
			}
		}
	}()
	defer func() {
		h.live.unregisterWS(slug, cli)
		close(cli.done)
		<-writerDone
	}()

	for {
		_ = ws.SetReadDeadline(time.Now().Add(wsReadTimeout))
		var msg string
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				_ = err
			}
			return
		}
		h.handleLiveWSInbound(slug, cli, msg)
	}
}

func (h *Handler) handleLiveWSInbound(slug string, cli *wsClient, msg string) {
	msg = strings.TrimSpace(msg)
	switch {
	case msg == "ping" || msg == "hb":
		cli.trySend("pong")
	case strings.HasPrefix(msg, "act:"):
		sec, err := strconv.Atoi(strings.TrimPrefix(msg, "act:"))
		if err != nil {
			return
		}
		if sec <= 0 {
			h.live.setActivity(slug, cli, 0)
			return
		}
		if sec > maxActivitySec {
			sec = maxActivitySec
		}
		h.live.setActivity(slug, cli, time.Duration(sec)*time.Second)
	default:
		logx.LogGateway("[artifacts] live ws (%s) ignored frame %q", slug, msg)
	}
}
