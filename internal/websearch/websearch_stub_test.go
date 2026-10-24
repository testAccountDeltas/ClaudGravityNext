package websearch

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

type stubRT func(*http.Request) (*http.Response, error)

func (f stubRT) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func resp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

// recorder is a Google/Wikipedia stub that records every outbound request.
type recorder struct {
	mu   sync.Mutex
	reqs []*http.Request
}

func (r *recorder) client(googleStatus int, googleBody string) *http.Client {
	return &http.Client{Transport: stubRT(func(req *http.Request) (*http.Response, error) {
		r.mu.Lock()
		r.reqs = append(r.reqs, req)
		r.mu.Unlock()
		if strings.Contains(req.URL.Host, "googleapis.com") {
			return resp(googleStatus, googleBody), nil
		}
		return resp(200, `{"query":{"search":[{"title":"Go","snippet":"a <b>language</b>"}]}}`), nil
	})}
}

func (r *recorder) googleCalls() (n int) {
	for _, q := range r.reqs {
		if strings.Contains(q.URL.Host, "googleapis.com") {
			n++
		}
	}
	return
}

const groundedResp = `{"response":{"candidates":[{"content":{"parts":[{"text":"Go is a language."}]},
"groundingMetadata":{"groundingChunks":[{"web":{"uri":"https://go.dev","title":"Go"}}]}}]}}`

func TestPerformWebSearch_GroundingUsesBearerToken(t *testing.T) {
	rec := &recorder{}
	res, sum, err := PerformWebSearch(context.Background(), "golang", "tok-123", true, true, "proj", rec.client(200, groundedResp), "UA")
	if err != nil || len(res) != 1 || res[0].URL != "https://go.dev" || sum == "" {
		t.Fatalf("res=%v sum=%q err=%v", res, sum, err)
	}
	if rec.googleCalls() != 1 {
		t.Fatalf("google calls %d", rec.googleCalls())
	}
	if got := rec.reqs[0].Header.Get("Authorization"); got != "Bearer tok-123" {
		t.Fatalf("Authorization %q", got)
	}
}

// A failed token refresh yields an empty token: it must never be sent to
// Google as "Bearer " — the search degrades to the public fallback.
func TestPerformWebSearch_EmptyTokenNeverReachesGoogle(t *testing.T) {
	rec := &recorder{}
	res, _, err := PerformWebSearch(context.Background(), "golang", "", true, true, "proj", rec.client(200, groundedResp), "UA")
	if err != nil || len(res) == 0 {
		t.Fatalf("res=%v err=%v", res, err)
	}
	if rec.googleCalls() != 0 {
		t.Fatalf("empty token request reached Google (%d calls)", rec.googleCalls())
	}
	for _, q := range rec.reqs {
		if a := q.Header.Get("Authorization"); a != "" {
			t.Fatalf("unexpected Authorization %q to %s", a, q.URL.Host)
		}
	}
}

func TestPerformWebSearch_APIKeyTenantSkipsGoogle(t *testing.T) {
	rec := &recorder{}
	_, _, err := PerformWebSearch(context.Background(), "q", "some-token", false, false, "p", rec.client(200, groundedResp), "UA")
	if err != nil || rec.googleCalls() != 0 {
		t.Fatalf("err=%v google calls=%d", err, rec.googleCalls())
	}
}

func TestPerformWebSearch_GoogleErrorFallsBack(t *testing.T) {
	rec := &recorder{}
	res, _, err := PerformWebSearch(context.Background(), "q", "tok", true, true, "p", rec.client(500, `{"error":"boom"}`), "UA")
	if err != nil || len(res) == 0 || res[0].Title != "Go" {
		t.Fatalf("res=%v err=%v", res, err)
	}
}

func directBody(stream bool) []byte {
	s := "false"
	if stream {
		s = "true"
	}
	return []byte(`{"model":"claude-3-5-haiku-20241022","stream":` + s + `,"tools":[{"name":"web_search"}],"messages":[{"role":"user","content":"Perform a web search for the query: golang"}]}`)
}

func TestDirectWebSearch_SSEEventShape(t *testing.T) {
	rec := &recorder{}
	w := httptest.NewRecorder()
	HandleClaudeCodeDirectWebSearch(context.Background(), 1, time.Now(), "k", "acc", "tok", true, true, "p",
		rec.client(200, groundedResp), "UA", directBody(true), "golang", w, nil)
	var events []string
	for _, l := range strings.Split(w.Body.String(), "\n") {
		if strings.HasPrefix(l, "event: ") {
			events = append(events, strings.TrimPrefix(l, "event: "))
		}
	}
	want := "message_start,content_block_start,content_block_delta,content_block_stop,content_block_start,content_block_stop,content_block_start,content_block_delta,content_block_stop,message_delta,message_stop"
	if strings.Join(events, ",") != want {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(events, ","), want)
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("content-type %q", w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), `"web_search_requests":1`) || !strings.Contains(w.Body.String(), "srvtoolu_") {
		t.Fatalf("body: %s", w.Body.String())
	}
}

func TestDirectWebSearch_NonStreamShape(t *testing.T) {
	rec := &recorder{}
	w := httptest.NewRecorder()
	HandleClaudeCodeDirectWebSearch(context.Background(), 1, time.Now(), "k", "acc", "", true, false, "p",
		rec.client(200, groundedResp), "UA", directBody(false), "golang", w, nil)
	b := w.Body.String()
	if gjson.Get(b, "content.0.type").String() != "server_tool_use" ||
		gjson.Get(b, "content.1.type").String() != "web_search_tool_result" ||
		gjson.Get(b, "content.2.type").String() != "text" ||
		gjson.Get(b, "stop_reason").String() != "end_turn" {
		t.Fatalf("body: %s", b)
	}
	if rec.googleCalls() != 0 {
		t.Fatal("no account: Google must not be called")
	}
}
