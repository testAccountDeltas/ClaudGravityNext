// Regression test for a real bug found live this session (see
// ROADMAP_PROGRESS.md's Phase 5 write-up): triggerProjectIDProbe used to
// set acc.ProjectIDProbed = true unconditionally, even when the probe
// itself failed — permanently burning the one-shot retry gate on a
// single transient network error (every account in a real pool ended
// up stuck on fallbackProjectID forever). This test fails on the old,
// unconditional-latch behavior and passes on the fix.
package accounts

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func jsonResponse(r *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    r,
	}
}

// roundTripperFunc adapts a plain function to http.RoundTripper, so each
// test can stub discoverAntigravityProjectID's two real endpoints
// without a live network call.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// withStubbedHTTPClient temporarily swaps the package-level httpClient
// used by discoverAntigravityProjectID/onboardAntigravityUser, restoring
// the original on test cleanup.
func withStubbedHTTPClient(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	orig := httpClient
	httpClient = &http.Client{Transport: rt}
	t.Cleanup(func() { httpClient = orig })
}

func waitForProbeToFinish(t *testing.T, email string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, inFlight := projectIDProbeInFlight.Load(email); !inFlight {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("probe for %s never finished within 5s", email)
}

func TestTriggerProjectIDProbe_TransientFailureDoesNotBurnTheGate(t *testing.T) {
	const email = "probe-fail@example.com"
	acc := &Record{Email: email, AccessToken: "tok", ProjectID: fallbackProjectID}
	seedAccounts(t, acc)

	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, &timeoutError{}
	}))

	globalAccountManager.triggerProjectIDProbe(email, "tok")
	waitForProbeToFinish(t, email)

	globalAccountManager.mu.RLock()
	probed := acc.ProjectIDProbed
	projectID := acc.ProjectID
	globalAccountManager.mu.RUnlock()

	if probed {
		t.Fatalf("ProjectIDProbed must stay false after a transient failure, so the NEXT token refresh retries instead of giving up forever — got true")
	}
	if projectID != fallbackProjectID {
		t.Fatalf("ProjectID must be left untouched on failure, got %q", projectID)
	}

	// The gate being false means a second trigger (e.g. the next real
	// RefreshAccessToken call) is free to try again, not permanently
	// skipped — confirm triggerProjectIDProbe doesn't short-circuit on
	// acc.ProjectIDProbed this time.
	var secondAttemptMade bool
	var mu sync.Mutex
	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		secondAttemptMade = true
		mu.Unlock()
		return nil, &timeoutError{}
	}))
	globalAccountManager.triggerProjectIDProbe(email, "tok")
	waitForProbeToFinish(t, email)

	mu.Lock()
	defer mu.Unlock()
	if !secondAttemptMade {
		t.Fatal("a second triggerProjectIDProbe call after a failed first attempt must actually retry (ProjectIDProbed must still be false) — the old bug made this a permanent no-op")
	}
}

func TestTriggerProjectIDProbe_SuccessLatchesTheGate(t *testing.T) {
	const email = "probe-success@example.com"
	acc := &Record{Email: email, AccessToken: "tok", ProjectID: fallbackProjectID}
	seedAccounts(t, acc)

	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(r, `{"cloudaicompanionProject":"real-project-123"}`), nil
	}))

	globalAccountManager.triggerProjectIDProbe(email, "tok")
	waitForProbeToFinish(t, email)

	globalAccountManager.mu.RLock()
	probed := acc.ProjectIDProbed
	projectID := acc.ProjectID
	globalAccountManager.mu.RUnlock()

	if !probed {
		t.Fatal("ProjectIDProbed must be true after a genuine success — a real discovered project id was found")
	}
	if projectID != "real-project-123" {
		t.Fatalf("ProjectID must be updated to the discovered value, got %q", projectID)
	}
}

type timeoutError struct{}

func (e *timeoutError) Error() string   { return "stub: simulated transient network failure" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }
