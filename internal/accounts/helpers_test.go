package accounts

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tidwall/sjson"
)

// resp builds a stub HTTP response.
func resp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// countingStub installs an httpClient whose every request is answered by fn
// and counted. The returned counter reads the number of calls so far.
func countingStub(t *testing.T, fn func(*http.Request) (*http.Response, error)) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		n.Add(1)
		return fn(r)
	}))
	return &n
}

// freshSelector swaps the global selector for an empty one for the test.
func freshSelector(t *testing.T) *AccountSelector {
	t.Helper()
	orig := globalSelector
	globalSelector = NewAccountSelector()
	t.Cleanup(func() { globalSelector = orig })
	return globalSelector
}

// fakeRefreshClock installs a controllable clock for RefreshAccessToken and
// clears the dead-token cache before and after. The returned counter is the
// number of nanoseconds the clock is advanced past base.
func fakeRefreshClock(t *testing.T, base time.Time) *atomic.Int64 {
	t.Helper()
	var off atomic.Int64
	origNow := refreshNow
	refreshNow = func() time.Time { return base.Add(time.Duration(off.Load())) }
	clearDead := func() {
		deadRefreshTokensMu.Lock()
		deadRefreshTokens = make(map[string]time.Time)
		deadRefreshTokensMu.Unlock()
	}
	clearDead()
	t.Cleanup(func() { refreshNow = origNow; clearDead() })
	return &off
}

// writeAuthFile writes a fake account JSON into dir and returns its path.
func writeAuthFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func setKey(key string, v interface{}) func([]byte) ([]byte, error) {
	return func(d []byte) ([]byte, error) { return sjson.SetBytes(d, key, v) }
}

func setRefresh(rt string) func([]byte) ([]byte, error) { return setKey("refresh_token", rt) }
