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
	// Wait for the background goroutine to finish (it clears its in-flight
	// marker last), so it cannot overlap with whatever test runs next.
	eventually(t, "the stale account's refresh", func() bool {
		_, running := quotaRefreshInFlight.Load("qr-stale@example.com")
		return n.Load() >= 1 && !running
	})
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

func TestRefreshAllQuotasStaggered_AsksEveryAccountOneAfterAnother(t *testing.T) {
	seedAccounts(t, catalogAccount("st-a@example.com"), catalogAccount("st-b@example.com"), catalogAccount("st-c@example.com"))
	var inFlight, maxInFlight, calls atomic.Int32
	withStubbedHTTPClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			m := maxInFlight.Load()
			if n <= m || maxInFlight.CompareAndSwap(m, n) {
				break
			}
		}
		calls.Add(1)
		time.Sleep(5 * time.Millisecond)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"groups":[]}`)), Header: http.Header{}}, nil
	}))
	globalAccountManager.RefreshAllQuotasStaggered(time.Millisecond, 3*time.Millisecond)
	if calls.Load() < 3 {
		t.Fatalf("every account must be asked: %d calls", calls.Load())
	}
	if maxInFlight.Load() != 1 {
		t.Fatalf("the accounts must be asked one at a time, max in flight = %d", maxInFlight.Load())
	}
}
