package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claudgravity/internal/logx"
	"claudgravity/internal/requestlog"
)

// D5: oversize bodies are 413 request_too_large on every body-reading
// endpoint, decided before any upstream call.
func TestBodyCap_413OnAllBodyEndpoints(t *testing.T) {
	e := newE2E(t, 1, func(int, string) (int, string) { return 200, okTurn("x") })
	big := strings.Repeat("a", maxRequestBodyBytes+1)
	for _, p := range []string{"/v1/messages", "/v1/messages/count_tokens", "/v1/embeddings"} {
		st, body, _ := e.do(t, "POST", p, map[string]string{"x-api-key": e2eKey}, big)
		if st != http.StatusRequestEntityTooLarge || !strings.Contains(body, "request_too_large") {
			t.Errorf("%s: status %d body %.200s", p, st, body)
		}
	}
	if e.calls != 0 {
		t.Fatalf("oversize request reached upstream")
	}
}

// D7: a rejected key must never be stored verbatim in the request log or
// written to the gateway log.
func TestUnauthorizedKeyNotLoggedRaw(t *testing.T) {
	dir := t.TempDir()
	if err := requestlog.InitLogStore(filepath.Join(dir, "logs.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(requestlog.Close)
	logPath := filepath.Join(dir, "gw.log")
	logx.InitLogger(logPath)
	t.Cleanup(func() { logx.InitLogger("superrelay.log") })

	e := newE2E(t, 1, func(int, string) (int, string) { return 200, okTurn("x") })
	for _, raw := range []string{"sk-ant-SUPERSECRETVALUE1234567890", "short"} {
		st, _, _ := e.post(t, "/v1/messages", raw, strings.Replace(e2eReq, "%s", "", 1))
		if st != 401 {
			t.Fatalf("status %d", st)
		}
		rows, _ := requestlog.Query("", "", "", "", false, 50)
		b, _ := json.Marshal(rows)
		if strings.Contains(string(b), raw) {
			t.Fatalf("raw key %q stored in request log: %s", raw, b)
		}
		if len(rows) == 0 {
			t.Fatal("expected the 401 to be logged")
		}
		if data, err := os.ReadFile(logPath); err == nil && strings.Contains(string(data), raw) {
			t.Fatalf("raw key %q in gateway log", raw)
		}
	}
}

func TestRedactedKey(t *testing.T) {
	for in, want := range map[string]string{"": "<redacted>", "abc": "<redacted>", "abcd": "<redacted>", "abcdefgh": "abcd...(8)"} {
		if got := redactedKey(in); got != want {
			t.Errorf("redactedKey(%q)=%q want %q", in, got, want)
		}
	}
}
