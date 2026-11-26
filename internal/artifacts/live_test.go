package artifacts

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestLiveBootTokenWatchAndWebSocket(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "live.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	h := NewHandler(s, nil)
	mux := http.NewServeMux()
	h.Register(mux)
	h.RegisterViewer(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Publish v1 artifact.
	_, m, _ := do(t, mux, "POST", "/api/frame/deploy/direct", `{"title":"Live App","content":"<h1>v1</h1>"}`)
	slug := m["slug"].(string)

	// boot response must carry subscriptionToken + expiry + renewal enabled.
	code, boot, _ := do(t, mux, "GET", "/api/frame/"+slug+"?via=model_read", "")
	if code != http.StatusOK {
		t.Fatalf("boot status = %d", code)
	}
	tok, _ := boot["subscriptionToken"].(string)
	if !strings.HasPrefix(tok, "fl1_") {
		t.Fatalf("boot subscriptionToken = %q, want fl1_* token", tok)
	}
	if boot["watchTokenRenewEnabled"] != true {
		t.Fatalf("watchTokenRenewEnabled = %v, want true", boot["watchTokenRenewEnabled"])
	}

	// watch-token endpoint renews cleanly.
	code, wt, _ := do(t, mux, "POST", "/api/frame/watch-token/"+slug, "{}")
	if code != http.StatusOK || !strings.HasPrefix(wt["subscriptionToken"].(string), "fl1_") {
		t.Fatalf("watch-token: %d %v", code, wt)
	}

	// Connecting without a valid subscriptionToken must fail before upgrade (401).
	badCfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(srv.URL, "http")+"/edge-api/frame-live/"+slug+"/ws", "https://claude.ai")
	if err != nil {
		t.Fatal(err)
	}
	badCfg.Protocol = []string{"frame-live.v1", "bogus-token"}
	if _, err := websocket.DialConfig(badCfg); err == nil {
		t.Fatal("expected WS dial with bad token to fail")
	}

	// Connect with the boot token just like claude.exe (`["frame-live.v1", token]`).
	cfg, err := websocket.NewConfig("ws"+strings.TrimPrefix(srv.URL, "http")+"/edge-api/frame-live/"+slug+"/ws", "https://claude.ai")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Protocol = []string{"frame-live.v1", tok}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatalf("valid WS dial failed: %v", err)
	}
	defer ws.Close()
	if got := ws.Config().Protocol; len(got) != 1 || got[0] != "frame-live.v1" {
		t.Fatalf("selected subprotocol = %v, want [frame-live.v1]", got)
	}

	// Keepalive ping/hb should answer with "pong".
	if err := websocket.Message.Send(ws, "ping"); err != nil {
		t.Fatal(err)
	}
	var pong string
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := websocket.Message.Receive(ws, &pong); err != nil || pong != "pong" {
		t.Fatalf("ping reply = %q, err=%v", pong, err)
	}

	// Connect browser SSE stream on /code/artifact/{slug}/events.
	sseResp, err := http.Get(srv.URL + "/code/artifact/" + slug + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	sseLines := make(chan string, 32)
	go func() {
		sc := bufio.NewScanner(sseResp.Body)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			sseLines <- sc.Text()
		}
		close(sseLines)
	}()

	waitSSE := func(wantEvent string) string {
		t.Helper()
		deadline := time.After(3 * time.Second)
		curEvent := ""
		for {
			select {
			case line, ok := <-sseLines:
				if !ok {
					t.Fatalf("SSE stream closed before event %q", wantEvent)
				}
				if strings.HasPrefix(line, "event: ") {
					curEvent = strings.TrimPrefix(line, "event: ")
				} else if strings.HasPrefix(line, "data: ") && curEvent == wantEvent {
					return strings.TrimPrefix(line, "data: ")
				}
			case <-deadline:
				t.Fatalf("timed out waiting for SSE event %q", wantEvent)
			}
		}
	}

	stateData := waitSSE("state")
	if !strings.Contains(stateData, `"version":"1"`) {
		t.Fatalf("unexpected initial SSE state: %s", stateData)
	}

	// Activity frame act:120 -> browser receives activity: true; act:0 -> false.
	if err := websocket.Message.Send(ws, "act:120"); err != nil {
		t.Fatal(err)
	}
	if act := waitSSE("activity"); !strings.Contains(act, `"active":true`) {
		t.Fatalf("expected active:true SSE event, got %s", act)
	}
	if err := websocket.Message.Send(ws, "act:0"); err != nil {
		t.Fatal(err)
	}
	if act := waitSSE("activity"); !strings.Contains(act, `"active":false`) {
		t.Fatalf("expected active:false SSE event, got %s", act)
	}

	// Republish v2 -> CLI WS receives {"ver":"2"} and browser SSE receives version event.
	_, dep2, _ := do(t, mux, "POST", "/api/frame/deploy/direct", `{"slug":"`+slug+`","content":"<h1>v2</h1>"}`)
	if dep2["version"] != "2" {
		t.Fatalf("redeploy v2: %v", dep2)
	}
	var verMsg string
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := websocket.Message.Receive(ws, &verMsg); err != nil {
		t.Fatalf("WS receive version failed: %v", err)
	}
	if !strings.Contains(verMsg, `"ver":"2"`) {
		t.Fatalf("expected {\"ver\":\"2\"} on WS, got %q", verMsg)
	}
	verSSE := waitSSE("version")
	var verEvt struct {
		Version string `json:"version"`
		RawHTML string `json:"raw_html"`
	}
	if err := json.Unmarshal([]byte(verSSE), &verEvt); err != nil {
		t.Fatalf("unmarshal SSE version event: %v", err)
	}
	if verEvt.Version != "2" || verEvt.RawHTML != "<h1>v2</h1>" {
		t.Fatalf("expected v2 payload in SSE version event, got %+v", verEvt)
	}

	// Browser comment asking Claude -> notifies CLI WS {"kind":"comment"}, then CLI lists, replies (201) and resolves (200).
	code, cResp, _ := do(t, mux, "POST", "/code/artifact/"+slug+"/comments", `{"text":"Make buttons bigger","ask_claude":true}`)
	if code != http.StatusCreated || cResp["thread"] == nil {
		t.Fatalf("viewer post comment = %d %v", code, cResp)
	}
	var commentWake string
	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := websocket.Message.Receive(ws, &commentWake); err != nil {
		t.Fatalf("WS receive comment wake failed: %v", err)
	}
	if !strings.Contains(commentWake, `"kind":"comment"`) {
		t.Fatalf("expected comment wake on WS, got %q", commentWake)
	}

	code, listResp, rawList := do(t, mux, "GET", "/api/frame/comments/"+slug+"?names=1", "")
	if code != http.StatusOK {
		t.Fatalf("frame comments list = %d %s", code, rawList)
	}
	threads := listResp["threads"].([]any)
	if len(threads) != 1 {
		t.Fatalf("threads len = %d, want 1 (%s)", len(threads), rawList)
	}
	tid := threads[0].(map[string]any)["id"].(string)
	firstMsg := threads[0].(map[string]any)["comments"].([]any)[0].(map[string]any)
	if firstMsg["to_claude_at"] == nil || firstMsg["author"].(map[string]any)["account"] != HumanAccountID {
		t.Fatalf("unexpected comment wire format: %s", rawList)
	}

	code, repResp, _ := do(t, mux, "POST", "/api/frame/comments/"+slug+"/"+tid, `{"text":"Done, buttons enlarged","answers_summon":true}`)
	if code != http.StatusCreated || repResp["thread_id"] != tid || repResp["comment_id"] == "" {
		t.Fatalf("frame comment reply = %d %v", code, repResp)
	}
	// Browser SSE should receive comments update including Claude's reply.
	var sawReply bool
	for i := 0; i < 4; i++ {
		if cSSE := waitSSE("comments"); strings.Contains(cSSE, "Done, buttons enlarged") {
			var parsed struct {
				Threads []WireThread `json:"threads"`
			}
			_ = json.Unmarshal([]byte(cSSE), &parsed)
			if len(parsed.Threads) == 1 && len(parsed.Threads[0].Comments) == 2 {
				sawReply = true
				break
			}
		}
	}
	if !sawReply {
		t.Fatal("browser SSE did not receive Claude's comment reply")
	}

	code, resResp, _ := do(t, mux, "POST", "/api/frame/comments/"+slug+"/"+tid+"/session-resolve", `{"resolved":true}`)
	if code != http.StatusOK || resResp["resolved"] != true {
		t.Fatalf("frame comment resolve = %d %v", code, resResp)
	}
}
