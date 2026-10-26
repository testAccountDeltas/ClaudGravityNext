package accounts

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
)

func TestCov_ParseResetTime(t *testing.T) {
	if !parseResetTime("").IsZero() || !parseResetTime("garbage").IsZero() {
		t.Fatal("empty/malformed must be zero")
	}
	want := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	if got := parseResetTime("2026-03-04T05:06:07Z"); !got.Equal(want) {
		t.Fatalf("RFC3339: %v", got)
	}
	if got := parseResetTime("2026-03-04T05:06:07.5Z"); !got.Equal(want.Add(500 * time.Millisecond)) {
		t.Fatalf("nano: %v", got)
	}
	// Offsets are honoured.
	if got := parseResetTime("2026-03-04T07:06:07+02:00"); !got.Equal(want) {
		t.Fatalf("offset: %v", got)
	}
	// Date-only is not a valid reset time.
	if !parseResetTime("2026-03-04").IsZero() {
		t.Fatal("date-only must be zero")
	}
}

func TestCov_BindAffinityLRUEvictionAndRefresh(t *testing.T) {
	as := NewAccountSelector()
	now := time.Now()
	as.bindAffinityLocked("", "a", now)
	as.bindAffinityLocked("k", "", now)
	if len(as.affinityIdx) != 0 {
		t.Fatal("empty key/email must be ignored")
	}
	for i := 0; i < maxSessionAffinityEntries; i++ {
		as.bindAffinityLocked(fmt.Sprintf("k%d", i), "a@x", now)
	}
	if len(as.affinityIdx) != maxSessionAffinityEntries {
		t.Fatalf("size %d", len(as.affinityIdx))
	}
	// Promote k0 so k1 is now the LRU tail; rebind to a new email in place.
	as.bindAffinityLocked("k0", "b@x", now)
	as.bindAffinityLocked("new", "c@x", now)
	if len(as.affinityIdx) != maxSessionAffinityEntries {
		t.Fatalf("size after evict %d", len(as.affinityIdx))
	}
	if _, ok := as.affinityIdx["k1"]; ok {
		t.Fatal("LRU tail k1 should be evicted")
	}
	if as.GetAffinityAccount("k0") != "b@x" || as.GetAffinityAccount("new") != "c@x" {
		t.Fatal("rebound/new bindings must resolve")
	}
	as.RememberAffinity("", "x")
	as.RememberAffinity("rem", "d@x")
	if as.GetAffinityAccount("rem") != "d@x" {
		t.Fatal("RememberAffinity")
	}
	// Expired binding is not returned.
	as.mu.Lock()
	as.affinityIdx["rem"].Value.(*affinityLRUEntry).expiresAt = now.Add(-time.Second)
	as.mu.Unlock()
	if as.GetAffinityAccount("rem") != "" {
		t.Fatal("expired binding must not resolve")
	}
}

func TestCov_SetStickyLockedEvictionAndLazyInit(t *testing.T) {
	as := &AccountSelector{} // nil maps: lazy init path
	as.setStickyLocked("", "e")
	as.setStickyLocked("k", "")
	if as.stickyMap != nil {
		t.Fatal("empty args must not initialise")
	}
	as.setStickyLocked("k0", "a@x")
	if as.stickyMap["k0"] != "a@x" || as.stickyLRU == nil {
		t.Fatal("lazy init")
	}
	for i := 1; i < maxStickyMapEntries; i++ {
		as.setStickyLocked(fmt.Sprintf("k%d", i), "a@x")
	}
	as.setStickyLocked("k0", "b@x") // promote + overwrite
	as.setStickyLocked("extra", "c@x")
	if len(as.stickyMap) != maxStickyMapEntries {
		t.Fatalf("size %d", len(as.stickyMap))
	}
	if _, ok := as.stickyMap["k1"]; ok {
		t.Fatal("k1 (LRU tail) should be evicted")
	}
	if as.stickyMap["k0"] != "b@x" {
		t.Fatal("promoted key must survive with new email")
	}
	// Empty LRU but full map: falls back to deleting an arbitrary entry.
	as2 := &AccountSelector{stickyMap: map[string]string{}}
	for i := 0; i < maxStickyMapEntries; i++ {
		as2.stickyMap[fmt.Sprintf("z%d", i)] = "e"
	}
	as2.setStickyLocked("new", "e")
	if len(as2.stickyMap) != maxStickyMapEntries || as2.stickyMap["new"] != "e" {
		t.Fatalf("fallback eviction size %d", len(as2.stickyMap))
	}
}

func TestCov_DeriveConversationAnchor(t *testing.T) {
	if DeriveConversationAnchor(nil, "H", "") != "sid:H" || DeriveConversationAnchor(nil, "H", "i") != "sid:H:i" {
		t.Fatal("header")
	}
	if DeriveConversationAnchor([]byte(`{"metadata":{"session_id":"S"}}`), "", "") != "sid:S" {
		t.Fatal("metadata sid")
	}
	if DeriveConversationAnchor([]byte(`{"metadata":{"session_id":"S"}}`), "", "i") != "sid:S:i" {
		t.Fatal("metadata sid + instance")
	}
	if DeriveConversationAnchor([]byte(`{"messages":[]}`), "", "") != "" {
		t.Fatal("no user message -> empty")
	}
	notice := `{"messages":[{"role":"user","content":"[Gateway notice: dropped]"},{"role":"assistant","content":"x"},{"role":"user","content":[{"type":"image"},{"type":"text","text":"hello"}]}]}`
	plain := `{"messages":[{"role":"user","content":"hello"}]}`
	a, b := DeriveConversationAnchor([]byte(notice), "", ""), DeriveConversationAnchor([]byte(plain), "", "")
	if a == "" || a != b || !strings.HasPrefix(a, "conv:") {
		t.Fatalf("notice must be skipped: %q %q", a, b)
	}
	if got := DeriveConversationAnchor([]byte(plain), "", "sub1"); got != b+":sub1" {
		t.Fatalf("instance suffix: %q", got)
	}
	long1 := `{"messages":[{"role":"user","content":"` + strings.Repeat("a", 600) + `1"}]}`
	long2 := `{"messages":[{"role":"user","content":"` + strings.Repeat("a", 600) + `2"}]}`
	if DeriveConversationAnchor([]byte(long1), "", "") != DeriveConversationAnchor([]byte(long2), "", "") {
		t.Fatal("only first 500 chars hashed")
	}
}

type covBroadcaster struct{ deleted []string }

func (b *covBroadcaster) BroadcastAccountDeleted(e string) { b.deleted = append(b.deleted, e) }
func (b *covBroadcaster) BroadcastAccounts(_ []*Record)    {}

func TestCov_DeleteAccount(t *testing.T) {
	sel := freshSelector(t)
	dir := t.TempDir()
	fp := filepath.Join(dir, "a.json")
	if err := os.WriteFile(fp, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	seedAccounts(t, &Record{Email: "a@x", FilePath: fp}, &Record{Email: "gone@x", FilePath: filepath.Join(dir, "missing.json")}, &Record{Email: "nofile@x"})
	sel.mu.Lock()
	sel.setStickyLocked("key", "a@x")
	sel.mu.Unlock()
	sel.RememberAffinity("conv", "a@x")
	bc := &covBroadcaster{}
	SetBroadcaster(bc)
	t.Cleanup(func() { SetBroadcaster(nil) })

	if err := Manager().DeleteAccount("nope@x"); err == nil {
		t.Fatal("unknown account must error")
	}
	if err := Manager().DeleteAccount("a@x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(fp); !os.IsNotExist(err) {
		t.Fatal("file must be removed")
	}
	if _, ok := Manager().GetAccount("a@x"); ok {
		t.Fatal("still in pool")
	}
	if sel.GetAffinityAccount("conv") != "" || sel.stickyMap["key"] != "" {
		t.Fatal("bindings must be unstuck")
	}
	if err := Manager().DeleteAccount("gone@x"); err != nil {
		t.Fatal(err)
	}
	if err := Manager().DeleteAccount("nofile@x"); err != nil {
		t.Fatal(err)
	}
	if len(bc.deleted) != 3 || len(AllAccounts()) != 0 {
		t.Fatalf("broadcast %v, left %d", bc.deleted, len(AllAccounts()))
	}
}

func TestCov_InitAccountManagerAppliesOverridesAndLoads(t *testing.T) {
	oID, oSec, oUA := AntigravityClientID, AntigravityClientSecret, AntigravityUserAgent
	t.Cleanup(func() {
		AntigravityClientID, AntigravityClientSecret, AntigravityUserAgent = oID, oSec, oUA
		ResetForTest()
	})
	dir := t.TempDir()
	writeAuthFile(t, dir, "x.json", `{"email":"load@x","refresh_token":"r","access_token":"t"}`)
	cfg := config.ServerConfig{GoogleOAuthClientID: "cid", GoogleOAuthClientSecret: "sec", AntigravityUserAgent: "ua/1"}
	if err := InitAccountManager(dir, cfg); err != nil {
		t.Fatal(err)
	}
	if AntigravityClientID != "cid" || AntigravityClientSecret != "sec" || AntigravityUserAgent != "ua/1" {
		t.Fatal("overrides not applied")
	}
	if a, ok := Manager().GetAccount("load@x"); !ok || a.RefreshToken != "r" {
		t.Fatal("account not loaded")
	}
	// Empty cfg leaves overrides alone.
	if err := InitAccountManager(dir, config.ServerConfig{}); err != nil || AntigravityClientID != "cid" {
		t.Fatal("empty cfg must not reset")
	}
	ResetForTest()
	if len(AllAccounts()) != 0 {
		t.Fatal("ResetForTest")
	}
}

func TestCov_ClearTransientError(t *testing.T) {
	future := time.Now().Add(time.Hour)
	sticky := &Record{ErrorKind: ErrKindForbidden, ErrorDetail: "d", ErrorUntil: future}
	clearTransientErrorLocked(sticky)
	if sticky.ErrorKind != ErrKindForbidden {
		t.Fatal("sticky error within ErrorUntil must be kept")
	}
	expired := &Record{ErrorKind: ErrKindForbidden, ErrorDetail: "d", ErrorUntil: time.Now().Add(-time.Hour), ErrorAt: time.Now()}
	clearTransientErrorLocked(expired)
	if expired.ErrorKind != "" || expired.ErrorDetail != "" || !expired.ErrorAt.IsZero() {
		t.Fatal("expired sticky must clear")
	}
}

func covJSON(body string) *http.Response { return resp(200, body) }

func TestCov_DiscoverProjectID(t *testing.T) {
	cases := []struct {
		name, load string
		want       string
	}{
		{"string", `{"cloudaicompanionProject":"  proj-a "}`, "proj-a"},
		{"object", `{"cloudaicompanionProject":{"id":"proj-b"}}`, "proj-b"},
	}
	for _, c := range cases {
		withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Authorization") != "Bearer tok" {
				t.Errorf("auth header missing")
			}
			return covJSON(c.load), nil
		}))
		got, err := discoverAntigravityProjectID(context.Background(), "tok")
		if err != nil || got != c.want {
			t.Fatalf("%s: %q %v", c.name, got, err)
		}
	}
	if projectIDFromNode(gjson.Parse(`{"id":""}`)) != "" || projectIDFromNode(gjson.Parse(`5`)) != "" {
		t.Fatal("projectIDFromNode empties")
	}

	// loadCodeAssist HTTP error and transport error.
	withStubbedHTTPClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) { return resp(500, "boom"), nil }))
	if _, err := discoverAntigravityProjectID(context.Background(), "t"); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("http err: %v", err)
	}
	withStubbedHTTPClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("dial") }))
	if _, err := discoverAntigravityProjectID(context.Background(), "t"); err == nil || !strings.Contains(err.Error(), "loadCodeAssist") {
		t.Fatalf("transport err: %v", err)
	}
}

func TestCov_OnboardUser(t *testing.T) {
	var tier string
	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "loadCodeAssist") {
			return covJSON(`{"allowedTiers":[{"id":"free"},{"id":"pro-tier","isDefault":true}]}`), nil
		}
		b := make([]byte, 512)
		n, _ := r.Body.Read(b)
		tier = gjson.GetBytes(b[:n], "tierId").String()
		return covJSON(`{"done":true,"response":{"cloudaicompanionProject":{"id":"newproj"}}}`), nil
	}))
	got, err := discoverAntigravityProjectID(context.Background(), "t")
	if err != nil || got != "newproj" || tier != "pro-tier" {
		t.Fatalf("got %q tier %q err %v", got, tier, err)
	}

	// done without project id; HTTP error; transport error.
	for name, h := range map[string]func(*http.Request) (*http.Response, error){
		"noid": func(*http.Request) (*http.Response, error) { return covJSON(`{"done":true,"response":{}}`), nil },
		"http": func(*http.Request) (*http.Response, error) { return resp(503, "x"), nil },
		"net":  func(*http.Request) (*http.Response, error) { return nil, errors.New("down") },
	} {
		withStubbedHTTPClient(t, roundTripperFunc(h))
		if _, err := onboardAntigravityUser(context.Background(), "t", "legacy-tier", QuotaHosts[0]); err == nil {
			t.Fatalf("%s: expected error", name)
		}
	}

	// Not done: waits on the poll timer, a cancelled context aborts it.
	withStubbedHTTPClient(t, roundTripperFunc(func(*http.Request) (*http.Response, error) { return covJSON(`{"done":false}`), nil }))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := onboardAntigravityUser(ctx, "t", "legacy-tier", QuotaHosts[0]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	if _, err := onboardAntigravityUser(nil, "t", "x", QuotaHosts[0]); err == nil { //nolint:staticcheck
		t.Fatal("nil ctx must error building request")
	}
}

func TestCov_GlobalSelectorNotNil(t *testing.T) {
	if GlobalSelector() == nil {
		t.Fatal("nil")
	}
}
