package mitm

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestIsAnthropicHost_Table(t *testing.T) {
	cases := map[string]bool{
		"api.anthropic.com":          true,
		"api.anthropic.com:443":      true,
		"API.Anthropic.COM.":         true,
		"anthropic.com":              true,
		"claude.ai":                  true,
		"statsig.anthropic.com":      true,
		"anthropic.com.evil.net":     false,
		"evilanthropic.com":          false,
		"notclaude.ai":               false,
		"claude.ai.attacker.net:443": false,
		"":                           false,
		"example.com":                false,
	}
	for h, want := range cases {
		if got := IsAnthropicHost(h); got != want {
			t.Errorf("IsAnthropicHost(%q)=%v want %v", h, got, want)
		}
	}
}

// A lookalike host must never be intercepted with the gateway's MITM cert:
// it goes through the SSRF-checked raw-tunnel path (here refused, since
// the name does not resolve to a public address). A nil CA would panic if
// the intercept branch were taken.
func TestConnect_LookalikeHostNotIntercepted(t *testing.T) {
	req := httptest.NewRequest(http.MethodConnect, "http://anthropic.com.evil.localhost:443", nil)
	req.Host = "anthropic.com.evil.localhost:443"
	req.Header.Set("Proxy-Authorization", "Bearer sec")
	rec := httptest.NewRecorder()
	HandleConnectTunnel(rec, req, nil, nil, "sec", 8900)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 from the raw-tunnel SSRF check", rec.Code)
	}
}

func TestIdleCopy_CopiesAndStopsOnEOF(t *testing.T) {
	a1, a2 := net.Pipe()
	b1, b2 := net.Pipe()
	done := make(chan struct{})
	go func() { idleCopy(b1, a2, time.Second); close(done) }()
	go func() { _, _ = a1.Write([]byte("hello")); a1.Close() }()
	buf := make([]byte, 5)
	if _, err := io.ReadFull(b2, buf); err != nil || string(buf) != "hello" {
		t.Fatalf("read %q err %v", buf, err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("idleCopy did not return after source EOF")
	}
	b1.Close()
	b2.Close()
	a2.Close()
}
