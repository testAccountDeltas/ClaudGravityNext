package accounts

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

var refreshBase = time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)

// refreshAcc seeds one account with a refresh token and no cached access
// token. ProjectIDProbed is true so no background probe is started.
func refreshAcc(t *testing.T, email string) *Record {
	t.Helper()
	acc := &Record{Email: email, RefreshToken: "rt-old", ProjectID: "p", ProjectIDProbed: true}
	seedAccounts(t, acc)
	return acc
}

func okToken(token string, expiresIn string) func(*http.Request) (*http.Response, error) {
	return func(*http.Request) (*http.Response, error) {
		return resp(200, `{"access_token":"`+token+`","expires_in":`+expiresIn+`}`), nil
	}
}

func deadUntil(email string) (time.Time, bool) {
	deadRefreshTokensMu.Lock()
	defer deadRefreshTokensMu.Unlock()
	u, ok := deadRefreshTokens[email]
	return u, ok
}

func TestRefresh_429_ShortCooldownAndFailFast(t *testing.T) {
	fakeRefreshClock(t, refreshBase)
	sel := freshSelector(t)
	acc := refreshAcc(t, "r429@example.com")
	sel.mu.Lock()
	sel.setStickyLocked("key", acc.Email)
	sel.mu.Unlock()
	n := countingStub(t, func(*http.Request) (*http.Response, error) { return resp(429, `{"error":"slow"}`), nil })

	_, err := globalAccountManager.RefreshAccessToken(acc.Email)
	if err == nil || !strings.Contains(err.Error(), "refresh rate-limited (429)") {
		t.Fatalf("got %v", err)
	}
	if u, ok := deadUntil(acc.Email); !ok || !u.Equal(refreshBase.Add(10*time.Second)) {
		t.Fatalf("want 10s negative cache, got %v %v", u, ok)
	}
	if d := time.Until(acc.CooldownUntil); d < 8*time.Second || d > 11*time.Second {
		t.Fatalf("want ~10s account cooldown, got %v", d)
	}
	if _, stuck := sel.stickyMap["key"]; stuck {
		t.Fatal("429 must unstick the account")
	}
	if _, err2 := globalAccountManager.RefreshAccessToken(acc.Email); err2 == nil || n.Load() != 1 {
		t.Fatalf("second call must fail fast without HTTP: calls=%d err=%v", n.Load(), err2)
	}
	if k := ClassifyError(statusFromErr(err), err.Error()); k != ErrKindRateLimit {
		t.Fatalf("error must classify as rate limit, got %s", k)
	}
}

func TestRefresh_InvalidGrant_DeadTokenFiveMinutes(t *testing.T) {
	off := fakeRefreshClock(t, refreshBase)
	sel := freshSelector(t)
	acc := refreshAcc(t, "rdead@example.com")
	sel.mu.Lock()
	sel.setStickyLocked("key", acc.Email)
	sel.mu.Unlock()
	n := countingStub(t, func(*http.Request) (*http.Response, error) {
		return resp(400, `{"error":"invalid_grant"}`), nil
	})

	if _, err := globalAccountManager.RefreshAccessToken(acc.Email); err == nil || !strings.Contains(err.Error(), "refresh failed (400)") {
		t.Fatalf("got %v", err)
	}
	if u, _ := deadUntil(acc.Email); !u.Equal(refreshBase.Add(5 * time.Minute)) {
		t.Fatalf("want 5-minute dead token, got %v", u)
	}
	if d := time.Until(acc.CooldownUntil); d < 4*time.Minute+50*time.Second || d > 5*time.Minute+time.Second {
		t.Fatalf("want ~5m account cooldown, got %v", d)
	}
	if acc.ErrorKind != ErrKindTokenRevoked || !acc.ErrorUntil.Equal(refreshBase.Add(24*time.Hour)) {
		t.Fatalf("want sticky token_revoked for 24h, got %q until %v", acc.ErrorKind, acc.ErrorUntil)
	}
	if _, stuck := sel.stickyMap["key"]; stuck {
		t.Fatal("invalid_grant must unstick the account")
	}
	if _, err := globalAccountManager.RefreshAccessToken(acc.Email); err == nil || n.Load() != 1 {
		t.Fatalf("dead token must fail fast: calls=%d", n.Load())
	}
	off.Store(int64(5*time.Minute + time.Second)) // cache expired: a real attempt again
	_, _ = globalAccountManager.RefreshAccessToken(acc.Email)
	if n.Load() != 2 {
		t.Fatalf("after the 5-minute cooldown a new attempt is allowed, calls=%d", n.Load())
	}
}

func TestRefresh_OtherOAuthErrorsNotDead(t *testing.T) {
	fakeRefreshClock(t, refreshBase)
	acc := refreshAcc(t, "r500@example.com")
	countingStub(t, func(*http.Request) (*http.Response, error) { return resp(500, `{"error":"backend"}`), nil })
	if _, err := globalAccountManager.RefreshAccessToken(acc.Email); err == nil {
		t.Fatal("expected error")
	}
	if _, ok := deadUntil(acc.Email); ok {
		t.Fatal("a 500 must not poison the account")
	}
	if !acc.CooldownUntil.IsZero() {
		t.Fatal("a 500 must not cool the account")
	}
}

func TestRefresh_NetworkError_TenSecondNegativeCache(t *testing.T) {
	off := fakeRefreshClock(t, refreshBase)
	acc := refreshAcc(t, "rnet@example.com")
	n := countingStub(t, func(*http.Request) (*http.Response, error) { return nil, errors.New("dial tcp: connection refused") })
	if _, err := globalAccountManager.RefreshAccessToken(acc.Email); err == nil {
		t.Fatal("expected error")
	}
	if u, _ := deadUntil(acc.Email); !u.Equal(refreshBase.Add(10 * time.Second)) {
		t.Fatalf("want 10s, got %v", u)
	}
	_, _ = globalAccountManager.RefreshAccessToken(acc.Email)
	if n.Load() != 1 {
		t.Fatalf("blip must be negatively cached, calls=%d", n.Load())
	}
	off.Store(int64(11 * time.Second))
	_, _ = globalAccountManager.RefreshAccessToken(acc.Email)
	if n.Load() != 2 {
		t.Fatalf("cache must lapse after 10s, calls=%d", n.Load())
	}
}

func TestRefresh_CacheHonoursSixtySecondSafetyMargin(t *testing.T) {
	off := fakeRefreshClock(t, refreshBase)
	acc := refreshAcc(t, "rcache@example.com")
	acc.AccessToken = "cached"
	acc.ExpiresAt = refreshBase.Add(61 * time.Second)
	n := countingStub(t, okToken("fresh", "3600"))

	if tok, err := globalAccountManager.RefreshAccessToken(acc.Email); err != nil || tok != "cached" || n.Load() != 0 {
		t.Fatalf("61s left: want cached token, got %q err=%v calls=%d", tok, err, n.Load())
	}
	off.Store(int64(2 * time.Second)) // 59s left: inside the margin
	if tok, err := globalAccountManager.RefreshAccessToken(acc.Email); err != nil || tok != "fresh" || n.Load() != 1 {
		t.Fatalf("59s left: want refresh, got %q err=%v calls=%d", tok, err, n.Load())
	}
}

func TestRefresh_ExpiresInZeroFallsBackTo3599(t *testing.T) {
	fakeRefreshClock(t, refreshBase)
	for _, body := range []string{`"expires_in":0`, `"expires_in":-5`, `"x":1`} {
		acc := refreshAcc(t, "rexp@example.com")
		countingStub(t, func(*http.Request) (*http.Response, error) {
			return resp(200, `{"access_token":"tok",`+body+`}`), nil
		})
		if _, err := globalAccountManager.RefreshAccessToken(acc.Email); err != nil {
			t.Fatal(err)
		}
		if want := refreshBase.Add(3599 * time.Second); !acc.ExpiresAt.Equal(want) {
			t.Fatalf("%s: got expiry %v want %v", body, acc.ExpiresAt, want)
		}
	}
}

func TestRefresh_RotatedRefreshTokenPersisted(t *testing.T) {
	fakeRefreshClock(t, refreshBase)
	acc := refreshAcc(t, "rrot@example.com")
	acc.FilePath = writeAuthFile(t, t.TempDir(), "a.json", `{"email":"rrot@example.com","refresh_token":"rt-old","extra":"keep"}`)
	countingStub(t, func(r *http.Request) (*http.Response, error) {
		_ = r.ParseForm()
		_ = r.Body.Close()
		return resp(200, `{"access_token":"new","expires_in":3600,"refresh_token":"rt-new"}`), nil
	})
	if _, err := globalAccountManager.RefreshAccessToken(acc.Email); err != nil {
		t.Fatal(err)
	}
	if acc.RefreshToken != "rt-new" {
		t.Fatalf("memory not rotated: %q", acc.RefreshToken)
	}
	b := readFile(t, acc.FilePath)
	if !strings.Contains(b, `"rt-new"`) || !strings.Contains(b, `"keep"`) {
		t.Fatalf("file not updated or lost fields: %s", b)
	}
}

func TestRefresh_SendsCurrentRefreshToken(t *testing.T) {
	fakeRefreshClock(t, refreshBase)
	acc := refreshAcc(t, "rsend@example.com")
	var got string
	countingStub(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != GoogleTokenEndpoint {
			t.Errorf("unexpected URL %s", r.URL)
		}
		_ = r.ParseForm()
		got = r.PostForm.Get("refresh_token") + "|" + r.PostForm.Get("grant_type")
		return resp(200, `{"access_token":"a","expires_in":60}`), nil
	})
	if _, err := globalAccountManager.RefreshAccessToken(acc.Email); err != nil {
		t.Fatal(err)
	}
	if got != "rt-old|refresh_token" {
		t.Fatalf("form = %q", got)
	}
}

func TestRefresh_UnknownAccountAndNoRefreshToken(t *testing.T) {
	fakeRefreshClock(t, refreshBase)
	seedAccounts(t, &Record{Email: "norefresh@example.com"})
	if _, err := globalAccountManager.RefreshAccessToken("ghost@example.com"); err == nil {
		t.Fatal("unknown account must error")
	}
	_, err := globalAccountManager.RefreshAccessToken("norefresh@example.com")
	if err == nil || ClassifyError(0, err.Error()) != ErrKindTokenRevoked {
		t.Fatalf("no-refresh-token error must classify as revoked: %v", err)
	}
}

func TestRefresh_TwentyConcurrentCallersCollapseToOneHTTPCall(t *testing.T) {
	fakeRefreshClock(t, refreshBase)
	acc := refreshAcc(t, "rconc@example.com")
	n := countingStub(t, okToken("shared", "3600"))
	var wg sync.WaitGroup
	toks := make([]string, 20)
	for i := range toks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			toks[i], _ = globalAccountManager.RefreshAccessToken(acc.Email)
		}(i)
	}
	wg.Wait()
	if n.Load() != 1 {
		t.Fatalf("want exactly 1 HTTP call, got %d", n.Load())
	}
	for i, tk := range toks {
		if tk != "shared" {
			t.Fatalf("caller %d got %q", i, tk)
		}
	}
}
