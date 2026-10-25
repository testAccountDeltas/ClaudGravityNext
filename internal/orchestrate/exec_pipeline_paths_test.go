package orchestrate

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
	"claudgravity/internal/warmup"
)

const (
	locationGate = `{"error":{"code":400,"status":"FAILED_PRECONDITION","message":"User location is not supported for the API use."}}`
	tokenWall    = `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"The input token count exceeds the maximum number of tokens allowed 1048576."}}`
)

// hostLog records which upstream host each attempt went to.
type hostLog struct {
	mu    sync.Mutex
	hosts []string
}

func (h *hostLog) add(host string) {
	h.mu.Lock()
	h.hosts = append(h.hosts, host)
	h.mu.Unlock()
}

func TestExecute_LocationGateRotatesToTheNextHostOnTheSameAccount(t *testing.T) {
	var hl hostLog
	r := runExec(t, execOpts{nAcc: 2, up: func(n int, req *http.Request, _ string) (*http.Response, error) {
		hl.add(req.URL.Host)
		if n == 1 {
			return respBody(400, locationGate), nil
		}
		return goodResp()
	}})
	if !r.ok || r.status != 200 || r.attempts != 2 {
		t.Fatalf("ok=%v status=%d attempts=%d err=%v", r.ok, r.status, r.attempts, r.err)
	}
	if hl.hosts[0] == hl.hosts[1] {
		t.Fatalf("the retry must go to a different host, both used %s", hl.hosts[0])
	}
	if r.auths[0] != r.auths[1] {
		t.Fatal("a location gate is the host's verdict: the account must not be swapped")
	}
}

func TestExecute_LocationGateEverywhere_TriesEveryHostTwiceOnEachAccount(t *testing.T) {
	r := runExec(t, execOpts{nAcc: 2, up: func(int, *http.Request, string) (*http.Response, error) {
		return respBody(400, locationGate), nil
	}})
	perAccount := maxLocationGatePasses * len(warmup.MainInferenceHosts)
	if r.ok || r.attempts != 2*perAccount {
		t.Fatalf("want %d tries on each of the 2 accounts, got ok=%v attempts=%d", perAccount, r.ok, r.attempts)
	}
}

func TestExecute_NetworkFailureRotatesHostsThenGivesUpWithoutBlamingTheAccount(t *testing.T) {
	r := runExec(t, execOpts{nAcc: 2, up: func(int, *http.Request, string) (*http.Response, error) {
		return nil, fmt.Errorf("dial tcp: lookup daily-cloudcode-pa.googleapis.com: no such host")
	}})
	want := maxLocationGatePasses * len(warmup.MainInferenceHosts)
	if r.ok || r.status != http.StatusBadGateway || r.attempts != want {
		t.Fatalf("want %d host tries then 502: ok=%v status=%d attempts=%d err=%v", want, r.ok, r.status, r.attempts, r.err)
	}
	if len(coolingNow(t)) != 0 {
		t.Fatalf("a DNS/dial failure says nothing about an account, yet %v is cooling", coolingNow(t))
	}
}

func TestExecute_SessionTokenWallBumpsTheSessionAndRetriesOnTheSameAccount(t *testing.T) {
	sess := "sess-" + t.Name()
	t.Cleanup(func() { setSessionBump(sess, 0) })
	r := runExec(t, execOpts{nAcc: 2, up: func(n int, _ *http.Request, _ string) (*http.Response, error) {
		if n == 1 {
			return respBody(400, tokenWall), nil
		}
		return goodResp()
	}})
	if !r.ok || r.status != 200 || r.attempts != 2 {
		t.Fatalf("ok=%v status=%d attempts=%d err=%v", r.ok, r.status, r.attempts, r.err)
	}
	if r.auths[0] != r.auths[1] {
		t.Fatal("the wall is per session, not per account: retry on the same account")
	}
	if r.bodies[0] == r.bodies[1] {
		t.Fatal("the retry must carry a fresh session bucket")
	}
	if got := getSessionBump(sess); got != 1 {
		t.Fatalf("session generation = %d, want 1 remembered for later requests", got)
	}
}

func TestExecute_SessionTokenWallGivesUpAfterThreeBumps(t *testing.T) {
	sess := "sess-" + t.Name()
	t.Cleanup(func() { setSessionBump(sess, 0) })
	r := runExec(t, execOpts{nAcc: 2, up: func(int, *http.Request, string) (*http.Response, error) {
		return respBody(400, tokenWall), nil
	}})
	if r.ok || r.status != http.StatusBadRequest || r.attempts != 1+maxBumpsPerAttempt {
		t.Fatalf("want 1+%d tries then the 400: ok=%v status=%d attempts=%d", maxBumpsPerAttempt, r.ok, r.status, r.attempts)
	}
	if got := getSessionBump(sess); got != maxBumpsPerAttempt {
		t.Fatalf("session generation = %d, want %d", got, maxBumpsPerAttempt)
	}
}

func directOpts(streaming bool, key, base string, up func(int, *http.Request, string) (*http.Response, error)) execOpts {
	return execOpts{direct: true, streaming: streaming, up: up, tenant: func(tc *config.TenantConfig) {
		tc.GoogleAPIKey, tc.GoogleBaseURL = key, base
	}}
}

func TestExecute_DirectKey_BuildsPublicAPIURLs(t *testing.T) {
	for _, c := range []struct {
		name      string
		streaming bool
		key, base string
		wantURL   string
	}{
		{"non-streaming, custom base, key", false, "K1", "http://gemini.test", "http://gemini.test/v1beta/models/gemini-2.5-flash:generateContent?key=K1"},
		{"streaming, custom base, key", true, "K1", "http://gemini.test", "http://gemini.test/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse&key=K1"},
		{"default base, no key", false, "", "", "https://generativelanguage.googleapis.com/v1beta/models/gemini-2.5-flash:generateContent"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var gotURL, gotAuth string
			up := func(_ int, req *http.Request, _ string) (*http.Response, error) {
				gotURL, gotAuth = req.URL.String(), req.Header.Get("Authorization")
				if c.streaming {
					return respBody(200, tfSSE(tfWrap(tfGood))), nil
				}
				return goodResp()
			}
			r := runExec(t, directOpts(c.streaming, c.key, c.base, up))
			if !r.ok || r.status != 200 || r.attempts != 1 {
				t.Fatalf("ok=%v status=%d attempts=%d err=%v body=%s", r.ok, r.status, r.attempts, r.err, r.body)
			}
			if gotURL != c.wantURL {
				t.Fatalf("URL = %s\nwant %s", gotURL, c.wantURL)
			}
			if gotAuth != "" {
				t.Fatalf("a plain API key travels in the URL; no bearer token expected, got %q", gotAuth)
			}
		})
	}
}

func TestExecute_DirectKey_HasNoAccountPoolToRotate(t *testing.T) {
	r := runExec(t, directOpts(false, "K", "http://gemini.test", func(int, *http.Request, string) (*http.Response, error) {
		return respBody(503, rl503), nil
	}))
	if r.ok || r.status != 503 || r.attempts != 1 {
		t.Fatalf("a rejected direct call is final (nothing to rotate to): ok=%v status=%d attempts=%d", r.ok, r.status, r.attempts)
	}
	if !strings.Contains(fmt.Sprint(r.err), "503") {
		t.Fatalf("the upstream status must reach the caller: %v", r.err)
	}
}

func TestExecute_DirectKey_BadModelTurnIsStillRetried(t *testing.T) {
	bad := `{"response":{"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}]}}`
	r := runExec(t, directOpts(false, "K", "http://gemini.test", func(n int, _ *http.Request, _ string) (*http.Response, error) {
		if n == 1 {
			return respBody(200, bad), nil
		}
		return goodResp()
	}))
	if !r.ok || r.status != 200 || r.attempts != 2 {
		t.Fatalf("a garbage turn is the model's fault, not the key's: it must be retried. ok=%v status=%d attempts=%d err=%v", r.ok, r.status, r.attempts, r.err)
	}
}

// An error that arrives inside the stream AFTER content went out cannot change
// the HTTP status the client already got, but the status the gateway reports
// (logs, cooldown decisions) must reflect what actually happened.
func TestMidStream_InBandErrorStatusMapping(t *testing.T) {
	for _, c := range []struct {
		name, errJSON string
		want          int
	}{
		{"quota exhausted", `{"code":429,"status":"RESOURCE_EXHAUSTED","message":"quota"}`, http.StatusTooManyRequests},
		{"overloaded", `{"code":503,"status":"UNAVAILABLE","message":"overloaded"}`, http.StatusServiceUnavailable},
		{"deadline", `{"code":504,"status":"DEADLINE_EXCEEDED","message":"slow"}`, http.StatusGatewayTimeout},
		{"anything else", `{"code":500,"status":"INTERNAL","message":"boom"}`, http.StatusInternalServerError},
	} {
		t.Run(c.name, func(t *testing.T) {
			body := firstChunk + "data: {\"error\":" + c.errJSON + "}\n\n"
			r := runExec(t, execOpts{streaming: true, up: func(_ int, req *http.Request, _ string) (*http.Response, error) {
				return okResp(newCtxBody(req.Context(), "", nil, body)), nil
			}})
			if !r.ok || r.attempts != 1 || r.status != c.want {
				t.Fatalf("ok=%v attempts=%d status=%d, want status %d\n%s", r.ok, r.attempts, r.status, c.want, r.body)
			}
			assertTerminalError(t, r.body)
		})
	}
}

// modelCoolingNow lists accounts with a live per-model cooldown.
func modelCoolingNow() []string {
	var out []string
	for _, a := range accounts.Manager().GetAccounts() {
		for _, until := range a.ModelCooldowns {
			if time.Now().Before(until) {
				out = append(out, a.Email)
				break
			}
		}
	}
	return out
}

func TestMidStream_QuotaExhaustedAfterContent_CoolsTheAccountDown(t *testing.T) {
	body := firstChunk + "data: {\"error\":{\"code\":429,\"status\":\"RESOURCE_EXHAUSTED\",\"message\":\"Resource has been exhausted (e.g. check quota).\"}}\n\n"
	r := runExec(t, execOpts{nAcc: 2, streaming: true, up: func(_ int, req *http.Request, _ string) (*http.Response, error) {
		return okResp(newCtxBody(req.Context(), "", nil, body)), nil
	}})
	if !r.ok || r.attempts != 1 || r.status != http.StatusTooManyRequests {
		t.Fatalf("ok=%v attempts=%d status=%d", r.ok, r.attempts, r.status)
	}
	// Google's message never names the status; without it the account would
	// stay in rotation and the NEXT request would hit the same wall.
	if cooling := modelCoolingNow(); len(cooling) != 1 {
		t.Fatalf("the exhausted account must be cooling down for this model, cooling=%v", cooling)
	}
}

func TestMidStream_PlainFailureAfterContentDoesNotCoolTheAccount(t *testing.T) {
	body := firstChunk + "data: {\"error\":{\"code\":500,\"status\":\"INTERNAL\",\"message\":\"boom\"}}\n\n"
	r := runExec(t, execOpts{nAcc: 2, streaming: true, up: func(_ int, req *http.Request, _ string) (*http.Response, error) {
		return okResp(newCtxBody(req.Context(), "", nil, body)), nil
	}})
	if !r.ok || r.status != http.StatusInternalServerError {
		t.Fatalf("ok=%v status=%d", r.ok, r.status)
	}
	if cooling := modelCoolingNow(); len(cooling) != 0 {
		t.Fatalf("a one-off internal error is not a quota signal, cooling=%v", cooling)
	}
}
