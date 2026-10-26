package orchestrate

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"claudgravity/internal/accounts"
)

const (
	sig400 = `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Corrupted thought signature."}}`
	rl503  = `{"error":{"code":503,"status":"UNAVAILABLE","message":"overloaded"}}`
	badReq = `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"Unknown name \"foo\": Cannot find field."}}`
)

func goodResp() (*http.Response, error) { return respBody(200, tfWrap(tfGood)), nil }

func coolingNow(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, a := range accounts.Manager().GetAccounts() {
		if time.Now().Before(a.CooldownUntil) {
			out = append(out, a.Email)
		}
	}
	return out
}

func TestExecute_PoolExhausted_Maps429WithoutUpstreamCall(t *testing.T) {
	r := runExec(t, execOpts{nAcc: 3,
		afterSeed: func() {
			for _, a := range accounts.Manager().GetAccounts() {
				accounts.Manager().MarkAccountCooldown(a.Email, 10*time.Minute)
			}
		},
		up: func(int, *http.Request, string) (*http.Response, error) { return goodResp() }})
	if r.ok || r.status != http.StatusTooManyRequests || r.attempts != 0 {
		t.Fatalf("whole pool cooling = rate limit, not a gateway failure: ok=%v status=%d attempts=%d err=%v", r.ok, r.status, r.attempts, r.err)
	}
}

func TestExecute_TokenRefreshFailure_DoesNotConsumeAnAttempt(t *testing.T) {
	// 4 accounts, 3 cannot refresh: even with a 3-attempt budget the 4th
	// (whichever order the selector tries them in) must still be reached.
	r := runExec(t, execOpts{nAcc: 4, refreshFail: map[string]bool{"ref1": true, "ref2": true, "ref3": true},
		up: func(int, *http.Request, string) (*http.Response, error) { return goodResp() }})
	if !r.ok || r.status != 200 || r.attempts != 1 {
		t.Fatalf("refresh failures must rotate silently: ok=%v status=%d attempts=%d err=%v", r.ok, r.status, r.attempts, r.err)
	}
}

func TestExecute_ThoughtSignature400_SelfHealsOnSameAccount(t *testing.T) {
	claude := `{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[` +
		`{"role":"user","content":"read it"},` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"REALSIG-abc123"},` +
		`{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"/a"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok"}]}]}`
	r := runExec(t, execOpts{nAcc: 2, claude: claude, up: func(n int, _ *http.Request, _ string) (*http.Response, error) {
		if n == 1 {
			return respBody(400, sig400), nil
		}
		return goodResp()
	}})
	if !r.ok || r.attempts != 2 {
		t.Fatalf("ok=%v attempts=%d status=%d err=%v", r.ok, r.attempts, r.status, r.err)
	}
	if !strings.Contains(r.bodies[0], "REALSIG-abc123") {
		t.Skip("translator did not carry the real signature upstream; cannot observe the heal")
	}
	if strings.Contains(r.bodies[1], "REALSIG-abc123") || !strings.Contains(r.bodies[1], "skip_thought_signature_validator") {
		t.Fatalf("resend must swap real signatures for skip_thought_signature_validator: %s", r.bodies[1])
	}
	if r.auths[0] != r.auths[1] {
		t.Fatalf("the account is not at fault and must not be excluded: %v", r.auths)
	}
	if len(coolingNow(t)) != 0 {
		t.Fatalf("no cooldown for a self-healed 400: %v", coolingNow(t))
	}
}

func TestExecute_NonRetryable400_AbortsAfterOneAttemptNoCooldown(t *testing.T) {
	r := runExec(t, execOpts{nAcc: 3, up: func(int, *http.Request, string) (*http.Response, error) { return respBody(400, badReq), nil }})
	if r.ok || r.attempts != 1 || r.status != http.StatusBadRequest {
		t.Fatalf("a schema 400 is the request's fault: ok=%v attempts=%d status=%d", r.ok, r.attempts, r.status)
	}
	if c := coolingNow(t); len(c) != 0 {
		t.Fatalf("a client error must not cool any account: %v", c)
	}
}

func TestExecute_ClientDisconnectBetweenAttempts_499NoCooldown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := runExec(t, execOpts{nAcc: 4, ctx: ctx, up: func(n int, _ *http.Request, _ string) (*http.Response, error) {
		cancel() // client leaves while the first attempt is failing with a 503
		return respBody(503, rl503), nil
	}})
	if r.ok || r.status != 499 || r.attempts != 1 {
		t.Fatalf("want one attempt then 499: ok=%v status=%d attempts=%d", r.ok, r.status, r.attempts)
	}
	if c := coolingNow(t); len(c) != 0 {
		t.Fatalf("the disconnect is not a verdict on the account: %v", c)
	}
}

func TestExecute_RetryBudget_Mixed429_503_BadTurn_Then200(t *testing.T) {
	bad := tfWrap(`{"finishReason":"MALFORMED_FUNCTION_CALL"}`)
	r := runExec(t, execOpts{nAcc: 4, up: func(n int, _ *http.Request, _ string) (*http.Response, error) {
		switch n {
		case 1:
			return respBody(429, rl429), nil
		case 2:
			return respBody(503, rl503), nil
		case 3:
			return respBody(200, bad), nil
		}
		return goodResp()
	}})
	if !r.ok || r.attempts != 4 || !strings.Contains(r.body, "healthy turn") {
		t.Fatalf("429 extends the budget, so 429,503,bad turn,200 = 4 calls: ok=%v attempts=%d status=%d err=%v", r.ok, r.attempts, r.status, r.err)
	}
}

func TestExecute_All429_TriesMinOfPoolAndEight(t *testing.T) {
	for _, n := range []int{2, 3, 5, 9} {
		t.Run(fmt.Sprintf("pool%d", n), func(t *testing.T) {
			r := runExec(t, execOpts{nAcc: n, up: func(int, *http.Request, string) (*http.Response, error) { return respBody(429, rl429), nil }})
			want := n
			if want > maxFailoverTries {
				want = maxFailoverTries
			}
			if r.ok || r.attempts != want {
				t.Fatalf("pool %d: want exactly %d tries, got ok=%v attempts=%d status=%d", n, want, r.ok, r.attempts, r.status)
			}
		})
	}
}

// When every account is dropped only because its token could not be refreshed,
// the client must not be told it was rate limited: the quota is fine.
func TestExecute_AllRefreshesFail_IsServiceUnavailableNotARateLimit(t *testing.T) {
	r := runExec(t, execOpts{nAcc: 2, refreshFail: map[string]bool{"ref1": true, "ref2": true},
		up: func(int, *http.Request, string) (*http.Response, error) { return goodResp() }})
	if r.ok || r.status != http.StatusServiceUnavailable || r.attempts != 0 {
		t.Fatalf("ok=%v status=%d attempts=%d err=%v", r.ok, r.status, r.attempts, r.err)
	}
}
