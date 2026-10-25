package accounts

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func resetOAuthStates(t *testing.T) {
	t.Helper()
	oauthStateMu.Lock()
	oauthStates = make(map[string]time.Time)
	oauthStateMu.Unlock()
}

func TestOAuthState_IsRandomHexAndSingleUse(t *testing.T) {
	resetOAuthStates(t)
	a, b := newOAuthState(), newOAuthState()
	re := regexp.MustCompile(`^[0-9a-f]{48}$`)
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Fatalf("state must be 48 hex chars, got %q %q", a, b)
	}
	if a == b {
		t.Fatal("two states must differ")
	}
	if !consumeOAuthState(a) {
		t.Fatal("a fresh state must be accepted once")
	}
	if consumeOAuthState(a) {
		t.Fatal("a replayed state must be refused")
	}
	if !consumeOAuthState(b) {
		t.Fatal("redeeming one state must not disturb another")
	}
}

func TestOAuthState_RejectsMissingUnknownAndExpired(t *testing.T) {
	resetOAuthStates(t)
	if consumeOAuthState("") {
		t.Fatal("empty state must be refused")
	}
	if consumeOAuthState("not-a-state") {
		t.Fatal("unknown state must be refused")
	}
	live := newOAuthState()
	if consumeOAuthState("not-a-state") || !consumeOAuthState(live) {
		t.Fatal("a failed attempt must not consume or invalidate a pending state")
	}

	oauthStateMu.Lock()
	oauthStates["stale"] = time.Now().Add(-time.Second)
	oauthStateMu.Unlock()
	if consumeOAuthState("stale") {
		t.Fatal("an expired state must be refused")
	}
	oauthStateMu.Lock()
	_, kept := oauthStates["stale"]
	oauthStateMu.Unlock()
	if kept {
		t.Fatal("an expired state must also be removed when presented")
	}
}

func TestOAuthState_TTLIsTenMinutes(t *testing.T) {
	resetOAuthStates(t)
	before := time.Now()
	s := newOAuthState()
	oauthStateMu.Lock()
	exp := oauthStates[s]
	oauthStateMu.Unlock()
	if d := exp.Sub(before); d < 9*time.Minute+50*time.Second || d > 10*time.Minute+10*time.Second {
		t.Fatalf("state lifetime = %v, want about 10 minutes", d)
	}
}

func TestOAuthState_MintingSweepsExpiredEntries(t *testing.T) {
	resetOAuthStates(t)
	oauthStateMu.Lock()
	for i := 0; i < 50; i++ {
		oauthStates["old"+string(rune('a'+i%26))+string(rune('A'+i/26))] = time.Now().Add(-time.Hour)
	}
	oauthStateMu.Unlock()
	newOAuthState()
	oauthStateMu.Lock()
	n := len(oauthStates)
	oauthStateMu.Unlock()
	if n != 1 {
		t.Fatalf("expired states must be swept when a new one is minted; %d left", n)
	}
}

func TestOAuthState_ConcurrentRedeemHasExactlyOneWinner(t *testing.T) {
	resetOAuthStates(t)
	s := newOAuthState()
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if consumeOAuthState(s) {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("a state must be redeemable exactly once, %d goroutines won", wins.Load())
	}
}

func TestBuildGoogleAuthURL(t *testing.T) {
	raw := BuildGoogleAuthURL("st&te=1 2")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != GoogleAuthEndpoint {
		t.Fatalf("endpoint = %s", got)
	}
	q := u.Query()
	for k, want := range map[string]string{
		"state": "st&te=1 2", "client_id": AntigravityClientID, "redirect_uri": OAuthRedirectURI,
		"response_type": "code", "access_type": "offline", "prompt": "consent",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	if !strings.Contains(q.Get("scope"), "auth/cloud-platform") || !strings.Contains(q.Get("scope"), "userinfo.email") {
		t.Errorf("scope lacks the required scopes: %q", q.Get("scope"))
	}
	if strings.Contains(raw, "client_secret") {
		t.Error("the client secret must never appear in the browser-facing URL")
	}
}

// exchangeEnv wires ExchangeAuthCode to a temp auth dir and a fake Google.
func exchangeEnv(t *testing.T, route func(*http.Request) (*http.Response, error)) string {
	t.Helper()
	dir := t.TempDir()
	am := globalAccountManager
	am.mu.Lock()
	origDir, origAccs := am.authDir, am.accounts
	am.authDir, am.accounts = dir, make(map[string]*Record)
	am.mu.Unlock()
	countingStub(t, route)
	t.Cleanup(func() { // runs BEFORE the stub is restored (LIFO)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			busy := false
			projectIDProbeInFlight.Range(func(_, _ any) bool { busy = true; return false })
			if !busy {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		am.mu.Lock()
		am.authDir, am.accounts = origDir, origAccs
		am.mu.Unlock()
	})
	return dir
}

func googleStub(token string, userinfo func() *http.Response, sawCode *string) func(*http.Request) (*http.Response, error) {
	return func(r *http.Request) (*http.Response, error) {
		switch {
		case r.URL.String() == GoogleTokenEndpoint:
			_ = r.ParseForm()
			if sawCode != nil {
				*sawCode = r.PostForm.Get("code") + "|" + r.PostForm.Get("grant_type") + "|" + r.PostForm.Get("client_id")
			}
			return resp(200, token), nil
		case strings.Contains(r.URL.Path, "/oauth2/v1/userinfo"):
			return userinfo(), nil
		}
		return resp(500, `{}`), nil // quota / project probes: irrelevant here
	}
}

func TestExchangeAuthCode_PersistsAccount(t *testing.T) {
	var saw string
	dir := exchangeEnv(t, googleStub(`{"access_token":"AT","refresh_token":"RT"}`,
		func() *http.Response { return resp(200, `{"email":"new@example.com"}`) }, &saw))
	rec, err := ExchangeAuthCode("the-code")
	if err != nil {
		t.Fatal(err)
	}
	if saw != "the-code|authorization_code|"+AntigravityClientID {
		t.Fatalf("token request carried %q", saw)
	}
	if rec.Email != "new@example.com" || rec.RefreshToken != "RT" || rec.AccessToken != "AT" {
		t.Fatalf("record = %+v", rec)
	}
	b, err := os.ReadFile(filepath.Join(dir, "antigravity-new@example.com.json"))
	if err != nil {
		t.Fatalf("auth file not written: %v", err)
	}
	var f map[string]any
	if err := json.Unmarshal(b, &f); err != nil || f["email"] != "new@example.com" || f["refresh_token"] != "RT" || f["type"] != "antigravity" {
		t.Fatalf("auth file = %s (%v)", b, err)
	}
	globalAccountManager.mu.RLock()
	_, registered := globalAccountManager.accounts["new@example.com"]
	globalAccountManager.mu.RUnlock()
	if !registered {
		t.Fatal("the new account must join the live pool")
	}
}

func TestExchangeAuthCode_Failures(t *testing.T) {
	for _, c := range []struct {
		name, token string
		tokenStatus int
		userinfo    string
		want        string
	}{
		{"token endpoint rejects the code", `{"error":"invalid_grant"}`, 400, `{"email":"x@example.com"}`, "code exchange failed"},
		{"userinfo has no email", `{"access_token":"AT","refresh_token":"RT"}`, 200, `{}`, "user email missing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := exchangeEnv(t, func(r *http.Request) (*http.Response, error) {
				switch {
				case r.URL.String() == GoogleTokenEndpoint:
					return resp(c.tokenStatus, c.token), nil
				case strings.Contains(r.URL.Path, "/oauth2/v1/userinfo"):
					return resp(200, c.userinfo), nil
				}
				return resp(500, `{}`), nil
			})
			rec, err := ExchangeAuthCode("c")
			if err == nil || !strings.Contains(err.Error(), c.want) || rec != nil {
				t.Fatalf("got (%v, %v), want error containing %q", rec, err, c.want)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("a failed exchange must not leave an auth file behind: %v", entries)
			}
		})
	}
}
