package accounts

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func quotaCounter(t *testing.T) *atomic.Int32 {
	t.Helper()
	var n atomic.Int32
	withStubbedHTTPClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		n.Add(1)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"groups":[]}`)), Header: http.Header{}}, nil
	}))
	return &n
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTriggerAsyncQuotaRefresh_OnlyWhenTheSnapshotIsStale(t *testing.T) {
	fresh := catalogAccount("qr-fresh@example.com")
	fresh.LastChecked = time.Now()
	stale := catalogAccount("qr-stale@example.com")
	stale.LastChecked = time.Now().Add(-time.Hour)
	seedAccounts(t, fresh, stale)
	n := quotaCounter(t)

	globalAccountManager.TriggerAsyncQuotaRefresh("qr-fresh@example.com", time.Minute)
	globalAccountManager.TriggerAsyncQuotaRefresh("nobody@example.com", time.Minute)
	time.Sleep(100 * time.Millisecond)
	if n.Load() != 0 {
		t.Fatalf("a fresh snapshot or an unknown account must not trigger a fetch, got %d", n.Load())
	}

	globalAccountManager.TriggerAsyncQuotaRefresh("qr-stale@example.com", time.Minute)
	eventually(t, "the stale account's refresh", func() bool { return n.Load() >= 1 })
}

func TestRefreshAllQuotas_FetchesEveryAccount(t *testing.T) {
	seedAccounts(t, catalogAccount("qa-1@example.com"), catalogAccount("qa-2@example.com"), catalogAccount("qa-3@example.com"))
	n := quotaCounter(t)
	globalAccountManager.RefreshAllQuotas()
	if n.Load() < 3 {
		t.Fatalf("expected at least one quota request per account, got %d", n.Load())
	}
	for _, a := range globalAccountManager.GetAccounts() {
		if a.LastChecked.IsZero() {
			t.Fatalf("%s was not refreshed", a.Email)
		}
	}
}
