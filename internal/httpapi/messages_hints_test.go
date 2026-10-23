package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClassifyReqClass(t *testing.T) {
	mk := func(hdr map[string]string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	title := []byte(`{"model":"claude-haiku-4-5","max_tokens":32,"system":"Generate a concise title","messages":[{"role":"user","content":"x"}]}`)
	work := []byte(`{"model":"claude-opus-4-5","max_tokens":16000,"system":"You are Claude Code","tools":[{"name":"Bash"}],"messages":[{"role":"user","content":"x"}]}`)
	if got := classifyReqClass(mk(nil), "", title); got != "auxiliary" {
		t.Errorf("title without hint = %q", got)
	}
	if got := classifyReqClass(mk(nil), "", work); got != "" {
		t.Errorf("real work = %q", got)
	}
	if got := classifyReqClass(mk(nil), "compaction", title); got != "compaction" {
		t.Errorf("explicit class overridden: %q", got)
	}
	if got := classifyReqClass(mk(map[string]string{"x-claude-code-parent-agent-id": "a1"}), "", work); got != "subagent" {
		t.Errorf("parent-agent-id = %q", got)
	}
}

func TestSetSSEHeaders(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil) // HTTP/1.1
	setSSEHeaders(w, r)
	h := w.Header()
	if h.Get("X-Accel-Buffering") != "no" || h.Get("Cache-Control") != "no-cache" || h.Get("Connection") != "keep-alive" {
		t.Errorf("headers = %v", h)
	}
	w = httptest.NewRecorder()
	r.ProtoMajor = 2
	setSSEHeaders(w, r)
	if w.Header().Get("Connection") != "" {
		t.Errorf("Connection must not be set on HTTP/2")
	}
}
