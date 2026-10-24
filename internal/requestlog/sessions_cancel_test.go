package requestlog

import (
	"testing"
	"time"
)

// A client hanging up (status 499) is not a gateway error and must not flag
// the session in the dashboard; a real failure still does.
func TestSessionHasErrors_IgnoresClientCancel(t *testing.T) {
	s := newTestStore(t)
	add := func(session string, status int) {
		s.Add(RequestLog{Timestamp: time.Now(), SessionID: session, KeyMasked: "k", Status: status, Account: "a@x"})
	}
	add("cancelled", 499)
	add("failed", 502)
	add("ok", 200)
	got := map[string]bool{}
	for _, sm := range s.QuerySessions(10) {
		got[sm.SessionID] = sm.HasErrors
	}
	if got["cancelled"] || !got["failed"] || got["ok"] {
		t.Fatalf("has_errors = %v, want cancelled=false failed=true ok=false", got)
	}
	if sm, _ := s.GetSessionDetail("cancelled"); sm == nil || sm.HasErrors {
		t.Fatalf("detail view must agree: %+v", sm)
	}
}
