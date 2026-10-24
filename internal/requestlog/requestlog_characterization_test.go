// Phase 1 characterization test for the ported requestlog package —
// proves the SQLite schema/queries, MaskKey, ParseAgentInfo's header
// priority, DeriveSessionID's fallback chain, and the broadcaster
// dependency-inversion fix all behave as documented.
package requestlog

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *SQLiteLogStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	if err := InitLogStore(path); err != nil {
		t.Fatalf("InitLogStore: %v", err)
	}
	t.Cleanup(globalLogStore.Close)
	return globalLogStore
}

func TestMaskKey_ShortKeysNeverVerbatim(t *testing.T) {
	cases := map[string]string{"": "***", "short": "***", "sk-12345": "sk...", "abcdefghijklmn": "ab..."}
	for in, want := range cases {
		if got := MaskKey(in); got != want {
			t.Fatalf("MaskKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskKey_LongKeyMasksMiddle(t *testing.T) {
	key := "sk-superrelay-0123456789abcdef"
	got := MaskKey(key)
	want := key[:8] + "..." + key[len(key)-4:]
	if got != want {
		t.Fatalf("MaskKey format changed from v1: got %q, want %q", got, want)
	}
}

func TestParseAgentInfo_MainHasNoInstanceID(t *testing.T) {
	r, _ := http.NewRequest("POST", "/v1/messages", nil)
	agentKey, agentType, _, instanceID := ParseAgentInfo(r)
	if agentKey != "main" || agentType != "main" {
		t.Fatalf("a request with no agent headers must classify as main, got key=%q type=%q", agentKey, agentType)
	}
	if instanceID != "" {
		t.Fatalf("main must never carry an instanceID (no real Agent-Id exists for it) — this was a real v1 cache-busting bug, got %q", instanceID)
	}
}

func TestParseAgentInfo_SubagentPrefersAgentIDOverPromptID(t *testing.T) {
	r, _ := http.NewRequest("POST", "/v1/messages", nil)
	r.Header.Set("x-claude-code-agent-type", "general-purpose")
	r.Header.Set("x-claude-code-prompt-id", "batch-123")
	r.Header.Set("x-claude-code-agent-id", "instance-456")

	agentKey, agentType, subagentName, instanceID := ParseAgentInfo(r)
	if agentKey != "sub:general-purpose" || agentType != "subagent" || subagentName != "general-purpose" {
		t.Fatalf("unexpected classification: key=%q type=%q name=%q", agentKey, agentType, subagentName)
	}
	if instanceID != "instance-456" {
		t.Fatalf("Agent-Id must take priority over Prompt-Id as the per-instance key, got %q", instanceID)
	}
}

func TestParseAgentInfo_AuxiliaryShortCircuits(t *testing.T) {
	r, _ := http.NewRequest("POST", "/v1/messages", nil)
	r.Header.Set("x-claude-code-request-class", "auxiliary")
	r.Header.Set("x-claude-code-agent-type", "should-be-ignored")

	agentKey, agentType, _, _ := ParseAgentInfo(r)
	if agentKey != "auxiliary" || agentType != "auxiliary" {
		t.Fatalf("auxiliary request-class must short-circuit before the agent-type branch, got key=%q type=%q", agentKey, agentType)
	}
}

func TestDeriveSessionID_HeaderTakesPriority(t *testing.T) {
	r, _ := http.NewRequest("POST", "/v1/messages", nil)
	r.Header.Set("x-session-id", "explicit-session-123")
	got := DeriveSessionID(r, []byte(`{"messages":[{"role":"user","content":"hi"}]}`), nil)
	if got != "explicit-session-123" {
		t.Fatalf("explicit x-session-id header must win over every fallback, got %q", got)
	}
}

func TestDeriveSessionID_FallsBackToTextHash(t *testing.T) {
	r, _ := http.NewRequest("POST", "/v1/messages", nil)
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello world"}]}]}`)
	got1 := DeriveSessionID(r, body, nil)
	got2 := DeriveSessionID(r, body, nil)
	if got1 != got2 {
		t.Fatalf("hashing the same first-user-turn text must be deterministic, got %q then %q", got1, got2)
	}
	if len(got1) < 6 || got1[:5] != "sess-" {
		t.Fatalf("hash-derived session id must use the sess- prefix as in v1, got %q", got1)
	}
}

func TestSQLiteLogStore_AddAndQueryRoundTrip(t *testing.T) {
	s := newTestStore(t)

	s.Add(RequestLog{
		Account:   "test@example.com",
		SessionID: "sess-abc",
		Model:     "gemini-2.5-pro",
		InTokens:  100,
		OutTokens: 50,
		Status:    200,
		Tools:     []string{"Read", "Bash"},
	})

	logs, stats := s.Query("", "", "", "", false, 10)
	if len(logs) != 1 {
		t.Fatalf("expected 1 logged request, got %d", len(logs))
	}
	if logs[0].AgentKey != "main" || logs[0].AgentType != "main" {
		t.Fatalf("Add must default empty AgentKey/AgentType to 'main' as in v1, got %q/%q", logs[0].AgentKey, logs[0].AgentType)
	}
	if len(logs[0].Tools) != 2 {
		t.Fatalf("tools_json round-trip failed: got %v", logs[0].Tools)
	}
	if stats.TotalRequests != 1 {
		t.Fatalf("expected stats.TotalRequests=1, got %d", stats.TotalRequests)
	}
}

func TestSQLiteLogStore_QuerySessionsAggregates(t *testing.T) {
	s := newTestStore(t)

	s.Add(RequestLog{SessionID: "sess-xyz", AgentType: "main", InTokens: 10, OutTokens: 5, Status: 200})
	s.Add(RequestLog{SessionID: "sess-xyz", AgentType: "subagent", SubagentName: "explore", InTokens: 20, OutTokens: 8, Status: 200})

	sessions := s.QuerySessions(10)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 distinct session, got %d", len(sessions))
	}
	sm := sessions[0]
	if sm.TotalRequests != 2 || sm.MainRequests != 1 || sm.SubRequests != 1 {
		t.Fatalf("session aggregation changed from v1: %+v", sm)
	}
	if sm.TotalInTokens != 30 || sm.TotalOutTokens != 13 {
		t.Fatalf("token sums changed from v1: in=%d out=%d", sm.TotalInTokens, sm.TotalOutTokens)
	}
}

func TestParseSQLiteTime_StripsMonotonicSuffix(t *testing.T) {
	// This exact format (with the " m=+..." monotonic suffix) is what
	// MIN()/MAX() forces a bound time.Time through as a plain string —
	// the real v1 bug this function exists to fix.
	raw := "2026-01-15 10:30:00.123456789 +0000 UTC m=+12345.000000001"
	got := parseSQLiteTime(raw)
	want := time.Date(2026, 1, 15, 10, 30, 0, 123456789, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("parseSQLiteTime must strip the monotonic suffix and parse correctly: got %v, want %v", got, want)
	}
}

type fakeBroadcaster struct {
	logCalls  int
	sessCalls int
	done      chan struct{}
}

func (f *fakeBroadcaster) BroadcastLog(entry *RequestLog) { f.logCalls++ }
func (f *fakeBroadcaster) BroadcastSessionsUpdate()       { f.sessCalls++; close(f.done) }

func TestSetBroadcaster_CalledOnAdd(t *testing.T) {
	s := newTestStore(t)
	fb := &fakeBroadcaster{done: make(chan struct{})}
	SetBroadcaster(fb)
	t.Cleanup(func() { SetBroadcaster(nil) })

	s.Add(RequestLog{SessionID: "sess-broadcast", Status: 200})

	select {
	case <-fb.done:
	case <-time.After(2 * time.Second):
		t.Fatal("broadcaster was not called within 2s of Add — dependency-inversion wiring broken")
	}
	if fb.logCalls != 1 {
		t.Fatalf("expected exactly 1 BroadcastLog call, got %d", fb.logCalls)
	}
}
