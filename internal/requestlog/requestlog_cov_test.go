package requestlog

import (
	"net/http"
	"strings"
	"testing"
)

func TestCov_DeriveSessionIDPriority(t *testing.T) {
	body := []byte(`{"metadata":{"session_id":"meta-1"},"messages":[{"role":"user","content":"hi"}]}`)
	headers := []string{"x-session-id", "x-claude-code-session-id", "anthropic-session-id", "x-anthropic-session-id"}
	// Each header wins over every later one, and over the body.
	for i, h := range headers {
		r, _ := http.NewRequest("POST", "/", nil)
		for _, later := range headers[i:] {
			r.Header.Set(later, "v-"+later)
		}
		if got := DeriveSessionID(r, body, nil); got != "v-"+h {
			t.Errorf("header %s: got %q", h, got)
		}
	}
	r, _ := http.NewRequest("POST", "/", nil)
	if got := DeriveSessionID(r, body, nil); got != "meta-1" {
		t.Fatalf("metadata session: %q", got)
	}
}

func TestCov_DeriveSessionIDTextHashAndGeminiFallback(t *testing.T) {
	r, _ := http.NewRequest("POST", "/", nil)
	str := []byte(`{"messages":[{"role":"assistant","content":"x"},{"role":"user","content":"hello"}]}`)
	arr := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	a, b := DeriveSessionID(r, str, nil), DeriveSessionID(r, arr, nil)
	if a != b || len(a) != len("sess-")+8 {
		t.Fatalf("string and block forms of the same text must agree: %q %q", a, b)
	}
	other := DeriveSessionID(r, []byte(`{"messages":[{"role":"user","content":"different"}]}`), nil)
	if other == a {
		t.Fatal("different first message must differ")
	}
	// User turn with empty text is skipped; next user turn is used.
	skip := DeriveSessionID(r, []byte(`{"messages":[{"role":"user","content":""},{"role":"user","content":"hello"}]}`), nil)
	if skip != a {
		t.Fatalf("empty user turn must be skipped: %q vs %q", skip, a)
	}
	// No usable messages: derived from the translated Gemini payload.
	gem := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello gemini"}]}]}}`)
	g1, g2 := DeriveSessionID(r, []byte(`{}`), gem), DeriveSessionID(r, []byte(`{}`), gem)
	if g1 != g2 || !strings.HasPrefix(g1, "sess-") || len(g1) > len("sess-")+8 || g1 == "sess-" {
		t.Fatalf("gemini fallback: %q %q", g1, g2)
	}
}

func TestCov_QueryFiltersAndLimit(t *testing.T) {
	s := newTestStore(t)
	s.Add(RequestLog{Key: "sk-aaaa-1111-bbbb", KeyMasked: "sk-a***bbbb", Account: "a@x", SessionID: "sess-one", AgentKey: "main", Status: 200, InTokens: 10, CacheHitRate: 0.5, TTFT: 100})
	s.Add(RequestLog{Key: "sk-cccc-2222-dddd", KeyMasked: MaskKey("sk-cccc-2222-dddd"), Account: "b@x", SessionID: "sess-two", AgentKey: "sub:explore", AgentType: "subagent", SubagentName: "explore", Status: 500, Error: "boom"})
	s.Add(RequestLog{Account: "a@x", SessionID: "sess-one", Status: 200, WasPanicked: true, Shrunk: true})

	count := func(key, acc, sess, agent string, onlyErr bool, limit int) int {
		logs, _ := s.Query(key, acc, sess, agent, onlyErr, limit)
		return len(logs)
	}
	if count("", "", "", "", false, 0) != 3 {
		t.Fatal("limit<=0 defaults to 100")
	}
	if count("", "", "", "", false, 1) != 1 {
		t.Fatal("limit honoured")
	}
	if count("", "", "", "", false, 100000) != 3 {
		t.Fatal("oversize limit clamps")
	}
	if count("cccc", "", "", "", false, 10) != 1 {
		t.Fatal("key filter matches the persisted masked key")
	}
	if count("2222", "", "", "", false, 10) != 0 {
		t.Fatal("raw key middle must never be persisted")
	}
	if count("sk-a***", "", "", "", false, 10) != 1 {
		t.Fatal("key filter matches masked key")
	}
	if count("", "a@x", "", "", false, 10) != 2 || count("", "nobody", "", "", false, 10) != 0 {
		t.Fatal("account filter exact")
	}
	if count("", "", "two", "", false, 10) != 1 {
		t.Fatal("session filter substring")
	}
	if count("", "", "", "explore", false, 10) != 1 || count("", "", "", "subagent", false, 10) != 1 || count("", "", "", "main", false, 10) != 2 {
		t.Fatal("agent filter by key/type/name")
	}
	logs, stats := s.Query("", "", "", "", true, 10)
	if len(logs) != 2 || stats.ErrorCount != 1 || stats.PanicCount != 1 || stats.TotalRequests != 3 || stats.ActiveSessions != 2 {
		t.Fatalf("only-errors: %d %+v", len(logs), stats)
	}
	for _, l := range logs {
		if l.Tools == nil {
			t.Fatal("Tools must be non-nil")
		}
	}
	if stats.AvgCacheHitRate != 0.5 || stats.AvgTTFTMs != 100 {
		t.Fatalf("averages: %+v", stats)
	}
	// Closed/never-initialised store answers empty, not a panic.
	var empty SQLiteLogStore
	if l, st := empty.Query("", "", "", "", false, 1); len(l) != 0 || st.TotalRequests != 0 {
		t.Fatal("nil db")
	}
}

func TestCov_PackageWrappers(t *testing.T) {
	newTestStore(t)
	Add(RequestLog{Account: "w@x", SessionID: "sess-w", Status: 200, InTokens: 5, OutTokens: 6})
	Add(RequestLog{Account: "w@x", SessionID: "sess-w", Status: 200, InTokens: 5, OutTokens: 6})
	if logs, _ := Query("", "w@x", "", "", false, 10); len(logs) != 2 {
		t.Fatalf("Query wrapper: %d", len(logs))
	}
	sums := QuerySessions(10)
	if len(sums) != 1 || sums[0].SessionID != "sess-w" || sums[0].TotalRequests != 2 || sums[0].TotalInTokens != 10 || sums[0].TotalOutTokens != 12 {
		t.Fatalf("QuerySessions wrapper: %+v", sums)
	}
	sum, logs := GetSessionDetail("sess-w")
	if sum == nil || len(logs) != 2 || sum.TotalRequests != 2 {
		t.Fatalf("GetSessionDetail wrapper: %+v %d", sum, len(logs))
	}
	if s2, l2 := GetSessionDetail("missing"); len(l2) != 0 && s2 != nil {
		t.Fatal("missing session")
	}
	Clear()
	if logs, _ := Query("", "", "", "", false, 10); len(logs) != 0 {
		t.Fatal("Clear wrapper")
	}
	if len(QuerySessions(10)) != 0 {
		t.Fatal("sessions after clear")
	}
}
