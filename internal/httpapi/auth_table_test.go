package httpapi

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"
)

func (e *e2eEnv) do(t *testing.T, method, path string, hdr map[string]string, body string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

func TestV1AuthTable(t *testing.T) {
	e := newE2E(t, 1, func(int, string) (int, string) { return 200, okTurn("x") })
	msgBody := strings.Replace(e2eReq, "%s", "", 1)
	endpoints := []struct{ method, path, body string }{
		{"POST", "/v1/messages", msgBody},
		{"POST", "/v1/messages/count_tokens", `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`},
		{"POST", "/v1/embeddings", `{"model":"gemini-embedding-001","input":"hi"}`},
		{"GET", "/v1/models", ""},
		{"GET", "/v1/models/claude-sonnet-4-5", ""},
	}
	creds := []struct {
		name string
		hdr  map[string]string
		ok   bool
	}{
		{"bearer", map[string]string{"Authorization": "Bearer " + e2eKey}, true},
		{"bearer lowercase scheme", map[string]string{"Authorization": "bearer " + e2eKey}, true},
		{"x-api-key", map[string]string{"x-api-key": e2eKey}, true},
		{"key without Bearer prefix", map[string]string{"Authorization": e2eKey}, true},
		{"x-api-key wins over bad Authorization", map[string]string{"x-api-key": e2eKey, "Authorization": "Bearer wrong"}, true},
		{"bad x-api-key wins over good Authorization", map[string]string{"x-api-key": "wrong", "Authorization": "Bearer " + e2eKey}, false},
		{"no credentials", nil, false},
		{"wrong key", map[string]string{"x-api-key": "wrong"}, false},
		{"basic scheme is not a key", map[string]string{"Authorization": "Basic " + e2eKey}, false},
		{"empty bearer", map[string]string{"Authorization": "Bearer "}, false},
	}
	for _, ep := range endpoints {
		for _, c := range creds {
			t.Run(ep.method+" "+ep.path+" / "+c.name, func(t *testing.T) {
				before := atomic.LoadInt32(&e.calls)
				st, body, h := e.do(t, ep.method, ep.path, c.hdr, ep.body)
				if c.ok {
					if st == 401 {
						t.Fatalf("authorized request rejected: %s", body)
					}
					return
				}
				if st != 401 {
					t.Fatalf("status %d, want 401: %s", st, body)
				}
				if gjson.Get(body, "type").String() != "error" || gjson.Get(body, "error.type").String() != "authentication_error" {
					t.Fatalf("body shape: %s", body)
				}
				if rid := h.Get("request-id"); !strings.HasPrefix(rid, "req_") {
					t.Fatalf("missing request-id header: %q", rid)
				}
				if gjson.Get(body, "request_id").String() != h.Get("request-id") {
					t.Fatalf("body request_id %q != header %q", gjson.Get(body, "request_id").String(), h.Get("request-id"))
				}
				if atomic.LoadInt32(&e.calls) != before {
					t.Fatal("unauthorized request reached upstream")
				}
			})
		}
	}
}
