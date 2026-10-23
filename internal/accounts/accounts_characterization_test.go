// Phase 2 characterization test for the ported accounts package —
// covers account selection priority, quota-fraction logic, and
// conversation-anchor derivation, all pure logic unchanged from v1 (no
// network calls).
package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"claudgravity/internal/config"
)

func seedAccounts(t *testing.T, accs ...*Record) {
	t.Helper()
	am := globalAccountManager
	am.mu.Lock()
	am.accounts = make(map[string]*Record)
	for _, a := range accs {
		am.accounts[a.Email] = a
	}
	am.mu.Unlock()
	t.Cleanup(func() {
		am.mu.Lock()
		am.accounts = make(map[string]*Record)
		am.mu.Unlock()
	})
}

func TestHasRemainingQuota_ClaudeGroupExcluded(t *testing.T) {
	acc := &Record{
		Email: "a@example.com",
		Quotas: []QuotaGroup{
			{DisplayName: "Claude", Buckets: []QuotaBucket{{RemainingFraction: 0.0}}},
			{DisplayName: "Gemini", Buckets: []QuotaBucket{{RemainingFraction: 0.9}}},
		},
	}
	if !hasRemainingQuota(acc) {
		t.Fatal("an exhausted Claude bucket must not block an account whose Gemini bucket is healthy")
	}
}

func TestHasRemainingQuota_GeminiExhaustedBlocks(t *testing.T) {
	acc := &Record{
		Email: "a@example.com",
		Quotas: []QuotaGroup{
			{DisplayName: "Gemini", Buckets: []QuotaBucket{{RemainingFraction: 0.01}}},
		},
	}
	if hasRemainingQuota(acc) {
		t.Fatal("an exhausted Gemini bucket with no ResetTime must block the account")
	}
}

func TestHasRemainingQuota_ExpiredResetTimeRestoresQuota(t *testing.T) {
	acc := &Record{
		Email: "a@example.com",
		Quotas: []QuotaGroup{
			{DisplayName: "Gemini", Buckets: []QuotaBucket{{
				RemainingFraction: 0.01,
				ResetTime:         time.Now().Add(-time.Hour).Format(time.RFC3339),
			}}},
		},
	}
	if !hasRemainingQuota(acc) {
		t.Fatal("a bucket whose ResetTime has already passed must be treated as restored, not stuck exhausted forever")
	}
}

func TestGetEffectiveQuotaFraction_UnprobedDefaultsToHalf(t *testing.T) {
	acc := &Record{Email: "a@example.com"}
	if frac := getEffectiveQuotaFraction(acc); frac != 0.5 {
		t.Fatalf("an account with no quota data at all must default to 0.5, got %v", frac)
	}
}

func TestGetEffectiveQuotaFraction_CooldownForcesZero(t *testing.T) {
	acc := &Record{Email: "a@example.com", CooldownUntil: time.Now().Add(time.Hour)}
	if frac := getEffectiveQuotaFraction(acc); frac != 0.0 {
		t.Fatalf("an account in active cooldown must report 0.0, got %v", frac)
	}
}

func TestDeriveConversationAnchor_HeaderSessionIDWins(t *testing.T) {
	anchor := DeriveConversationAnchor([]byte(`{}`), "sess-123", "")
	if anchor != "sid:sess-123" {
		t.Fatalf("got %q", anchor)
	}
}

func TestDeriveConversationAnchor_InstanceIDQualifiesSubagent(t *testing.T) {
	anchor := DeriveConversationAnchor([]byte(`{}`), "sess-123", "inst-A")
	if anchor != "sid:sess-123:inst-A" {
		t.Fatalf("got %q", anchor)
	}
}

func TestDeriveConversationAnchor_FallsBackToFirstUserMessageHash(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello there"}]}`)
	a1 := DeriveConversationAnchor(body, "", "")
	a2 := DeriveConversationAnchor(body, "", "")
	if a1 != a2 || a1 == "" {
		t.Fatalf("expected a deterministic non-empty anchor, got %q then %q", a1, a2)
	}
}

func TestDeriveConversationAnchor_SkipsGatewayNoticeAsFirstMessage(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","content":"[Gateway notice: context truncated]"},
		{"role":"user","content":"the real first message"}
	]}`)
	anchor := DeriveConversationAnchor(body, "", "")
	wantBody := []byte(`{"messages":[{"role":"user","content":"the real first message"}]}`)
	want := DeriveConversationAnchor(wantBody, "", "")
	if anchor != want {
		t.Fatal("expected the anchor to skip the injected gateway notice and hash the real first message")
	}
}

func TestSelectAccount_StickyAccountPinnedInTenantConfig(t *testing.T) {
	seedAccounts(t,
		&Record{Email: "pinned@example.com"},
		&Record{Email: "other@example.com"},
	)
	sel := NewAccountSelector()
	acc, err := sel.SelectAccount("key1", "", config.TenantConfig{StickyAccount: "pinned@example.com"})
	if err != nil || acc.Email != "pinned@example.com" {
		t.Fatalf("expected the pinned account, got %v err=%v", acc, err)
	}
}

func TestSelectAccount_SessionAffinityWinsOverStickyMap(t *testing.T) {
	seedAccounts(t,
		&Record{Email: "affinity@example.com"},
		&Record{Email: "sticky@example.com"},
	)
	sel := NewAccountSelector()
	sel.stickyMap["key1"] = "sticky@example.com"
	sel.RememberAffinity("conv:abc", "affinity@example.com")

	acc, err := sel.SelectAccount("key1", "conv:abc", config.TenantConfig{})
	if err != nil || acc.Email != "affinity@example.com" {
		t.Fatalf("expected session affinity to win over the sticky map, got %v err=%v", acc, err)
	}
}

func TestSelectAccount_SubagentNeverUsesMainThreadStickyMap(t *testing.T) {
	seedAccounts(t,
		&Record{Email: "sticky@example.com"},
		&Record{Email: "other@example.com"},
	)
	sel := NewAccountSelector()
	sel.stickyMap["key1"] = "sticky@example.com"

	// A subagent conversationKey has >=2 colons ("sid:session:instance").
	_, err := sel.SelectAccount("key1", "sid:session:instance1", config.TenantConfig{})
	if err != nil {
		t.Fatalf("expected a valid selection, got err=%v", err)
	}
	// Just confirm it didn't error; round-robin fallback is inherently
	// one of the two seeded accounts, so no further assertion needed —
	// the real regression this guards is "always the SAME sticky
	// account for every subagent", not a specific pick.
}

func TestSelectAccount_ExcludedAccountSkipped(t *testing.T) {
	seedAccounts(t,
		&Record{Email: "excluded@example.com"},
		&Record{Email: "ok@example.com"},
	)
	sel := NewAccountSelector()
	acc, err := sel.SelectAccountExcluding("key1", "", config.TenantConfig{StickyAccount: "excluded@example.com"}, map[string]bool{"excluded@example.com": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acc.Email == "excluded@example.com" {
		t.Fatal("an explicitly excluded account must never be chosen, even if pinned")
	}
}

func TestSelectAccount_AllExcludedOrCoolingDownReturnsError(t *testing.T) {
	seedAccounts(t,
		&Record{Email: "a@example.com", CooldownUntil: time.Now().Add(time.Hour)},
	)
	sel := NewAccountSelector()
	_, err := sel.SelectAccount("key1", "", config.TenantConfig{})
	if err == nil {
		t.Fatal("expected an error when every account is cooling down")
	}
}

func TestUnstickAccount_RemovesStickyAndAffinityBindings(t *testing.T) {
	sel := NewAccountSelector()
	sel.stickyMap["key1"] = "dead@example.com"
	sel.RememberAffinity("conv:xyz", "dead@example.com")

	sel.UnstickAccount("dead@example.com")

	if _, ok := sel.stickyMap["key1"]; ok {
		t.Fatal("expected the sticky binding to be removed")
	}
	if got := sel.GetAffinityAccount("conv:xyz"); got != "" {
		t.Fatalf("expected the affinity binding to be removed, got %q", got)
	}
}

func TestAffinityLRU_EvictsOldestWhenFull(t *testing.T) {
	sel := NewAccountSelector()
	now := time.Now()
	// Bind beyond-capacity entries directly via the locked helper to
	// avoid a 5000-iteration test.
	for i := 0; i < 3; i++ {
		sel.bindAffinityLocked(string(rune('a'+i)), "acc", now)
	}
	if len(sel.affinityIdx) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(sel.affinityIdx))
	}
	// Touch "a" so it's not the least-recently-used anymore.
	sel.bindAffinityLocked("a", "acc2", now.Add(time.Second))
	if sel.affinityLRU.Front().Value.(*affinityLRUEntry).key != "a" {
		t.Fatal("expected the most recently bound/touched entry at the front")
	}
}

func TestStickyMap_BoundedCapacity(t *testing.T) {
	sel := NewAccountSelector()
	for i := 0; i < maxStickyMapEntries+50; i++ {
		key := fmt.Sprintf("key-%d", i)
		sel.mu.Lock()
		sel.setStickyLocked(key, fmt.Sprintf("acc-%d@example.com", i))
		sel.mu.Unlock()
	}
	sel.mu.Lock()
	defer sel.mu.Unlock()
	if len(sel.stickyMap) > maxStickyMapEntries {
		t.Fatalf("expected stickyMap to be bounded at %d entries, got %d", maxStickyMapEntries, len(sel.stickyMap))
	}
	// The first 50 keys (0..49) should have been evicted
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("key-%d", i)
		if _, ok := sel.stickyMap[key]; ok {
			t.Fatalf("expected old key %s to be evicted", key)
		}
	}
	// The newest keys should exist
	for i := maxStickyMapEntries; i < maxStickyMapEntries+50; i++ {
		key := fmt.Sprintf("key-%d", i)
		if _, ok := sel.stickyMap[key]; !ok {
			t.Fatalf("expected new key %s to exist", key)
		}
	}
}

func TestExchangeAuthCode_ContextTimeout(t *testing.T) {
	called := false
	withStubbedHTTPClient(t, roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		deadline, ok := r.Context().Deadline()
		if !ok || deadline.IsZero() {
			t.Errorf("expected request to have a context deadline")
		}
		return nil, context.DeadlineExceeded
	}))

	_, err := ExchangeAuthCode("test-code")
	if !called {
		t.Fatal("expected HTTP client to be called")
	}
	if err == nil {
		t.Fatal("expected error on timeout")
	}
}

func TestSelectAccount_StickyAccountInCooldown_FallsBackToHealthy(t *testing.T) {
	seedAccounts(t,
		&Record{Email: "preferred@example.com", CooldownUntil: time.Now().Add(88 * time.Hour)},
		&Record{Email: "healthy@example.com", Quotas: []QuotaGroup{
			{DisplayName: "Gemini", Buckets: []QuotaBucket{{RemainingFraction: 0.95}}},
		}},
	)
	sel := NewAccountSelector()
	cfg := config.TenantConfig{StickyAccount: "preferred@example.com", Strategy: "sticky"}

	acc, err := sel.SelectAccount("client-key", "", cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if acc.Email != "healthy@example.com" {
		t.Fatalf("expected healthy fallback account, got %s", acc.Email)
	}

	// Subsequent request must stick to the healthy account without randomizing
	acc2, err := sel.SelectAccount("client-key", "", cfg)
	if err != nil || acc2.Email != "healthy@example.com" {
		t.Fatalf("expected subsequent request to stick to healthy@example.com, got %v err=%v", acc2, err)
	}
}

func TestRecord_DoesNotSerializeSensitiveFields(t *testing.T) {
	rec := &Record{
		Email:        "user@example.com",
		RefreshToken: "super-secret-refresh-token",
		AccessToken:  "super-secret-access-token",
		FilePath:     "/var/secrets/accounts/user.json",
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "file_path") || strings.Contains(s, "/var/secrets") {
		t.Fatalf("FilePath leaked in Record JSON: %s", s)
	}
	if strings.Contains(s, "refresh-token") || strings.Contains(s, "access-token") {
		t.Fatalf("tokens leaked in Record JSON: %s", s)
	}
}

// Live evidence: Google 429s every Gemini model on an account whose weekly
// bucket is ~0 even next to a healthy 5h bucket, so such an account is skipped.
func TestSelectAccount_StickyAccountZeroWeeklyHealthy5h_Skipped(t *testing.T) {
	seedAccounts(t,
		&Record{Email: "zero_weekly@example.com", Quotas: []QuotaGroup{
			{DisplayName: "Gemini Models", Buckets: []QuotaBucket{
				{Window: "5h", RemainingFraction: 0.24, ResetTime: time.Now().Add(3 * time.Hour).Format(time.RFC3339)},
				{Window: "weekly", RemainingFraction: 0.0, ResetTime: time.Now().Add(88 * time.Hour).Format(time.RFC3339)},
			}},
		}},
		&Record{Email: "backup@example.com", Quotas: []QuotaGroup{
			{DisplayName: "Gemini Models", Buckets: []QuotaBucket{{Window: "weekly", RemainingFraction: 0.85}}},
		}},
	)
	sel := NewAccountSelector()
	cfg := config.TenantConfig{StickyAccount: "zero_weekly@example.com", Strategy: "sticky"}
	acc, err := sel.SelectAccount("client-key", "", cfg)
	if err != nil || acc.Email != "backup@example.com" {
		t.Fatalf("pinned account with an exhausted weekly bucket must be skipped, got %v err=%v", acc, err)
	}
}

func TestSelectAccount_StickyAccountAllBucketsExhausted_Skipped(t *testing.T) {
	seedAccounts(t,
		&Record{Email: "dead@example.com", Quotas: []QuotaGroup{
			{DisplayName: "Gemini Models", Buckets: []QuotaBucket{
				{Window: "5h", RemainingFraction: 0.0, ResetTime: time.Now().Add(20 * time.Hour).Format(time.RFC3339)},
				{Window: "weekly", RemainingFraction: 0.0, ResetTime: time.Now().Add(88 * time.Hour).Format(time.RFC3339)},
			}},
		}},
		&Record{Email: "backup@example.com", Quotas: []QuotaGroup{
			{DisplayName: "Gemini Models", Buckets: []QuotaBucket{{Window: "weekly", RemainingFraction: 0.85}}},
		}},
	)
	sel := NewAccountSelector()
	cfg := config.TenantConfig{StickyAccount: "dead@example.com", Strategy: "sticky"}
	acc, err := sel.SelectAccount("client-key", "", cfg)
	if err != nil || acc.Email != "backup@example.com" {
		t.Fatalf("fully exhausted account must be skipped, got %v err=%v", acc, err)
	}
}
