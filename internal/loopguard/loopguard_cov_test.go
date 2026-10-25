package loopguard

import (
	"testing"
	"time"
)

func TestCovReadLoopEquivalentToolName(t *testing.T) {
	cases := []struct {
		sig         string
		name        string
		read, write bool
	}{
		{"Grep", "Grep", false, false},
		{"read", "read", false, false}, // no colon: exact "Read" only
		{`read:{"file_path":"a"}`, "read", true, false},
		{`PowerShell:{"command":"Get-Content a.txt"}`, "PowerShell", true, false},
		{`pwsh:{"command":"Set-Content -Path a.txt -Value x"}`, "pwsh", false, true},
		{`PowerShell:{"command":"Get-Date"}`, "PowerShell", false, false},
		{`Edit:{"file_path":"a"}`, "Edit", false, false},
		{`Bash:{"command":"ls"}`, "Bash", false, false},
	}
	for _, c := range cases {
		n, r, w := ReadLoopEquivalentToolName(c.sig)
		if n != c.name || r != c.read || w != c.write {
			t.Errorf("%q: got (%q,%v,%v) want (%q,%v,%v)", c.sig, n, r, w, c.name, c.read, c.write)
		}
	}
}

func TestCovExportedWrappers(t *testing.T) {
	if Global() == nil {
		t.Fatal("Global nil")
	}
	w, th := ReadLoopLimits("subagent", true)
	if w != exploreReadWindowSize || th != exploreReadWindowThreshold {
		t.Fatalf("bare subagent key must be relaxed: %d %d", w, th)
	}
	if !IsPlanModeRequest([]byte(`{"tools":[{"name":"ExitPlanMode"}]}`)) || IsPlanModeRequest([]byte(`{"tools":[{"name":"Edit"}]}`)) {
		t.Fatal("IsPlanModeRequest wrong")
	}
	used := []byte(`{"tools":[{"name":"Edit"}],"messages":[{"role":"assistant","content":[{"type":"tool_use","name":"Edit"}]}]}`)
	if !HasModifyingToolsInRequest(used) {
		t.Fatal("invoked Edit must count as modifying")
	}
}

func TestCovPendingNudgesGlobal(t *testing.T) {
	const key = "cov-pending-key"
	RecordPendingNudge(key, "a")
	RecordPendingNudge(key, "a") // dedupe
	RecordPendingNudge(key, "b")
	RecordPendingNudge("", "x")
	RecordPendingNudge(key+"2", "")
	got := TakePendingNudges(key)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v", got)
	}
	if again := TakePendingNudges(key); len(again) != 0 {
		t.Fatalf("must be cleared, got %v", again)
	}
	if TakePendingNudges("") != nil || TakePendingNudges(key+"2") != nil {
		t.Fatal("empty key/text must store nothing")
	}
}

func TestCovPendingNudgesNilMap(t *testing.T) {
	lg := &LoopGuardManager{lastSeen: map[string]time.Time{}}
	if lg.TakePendingNudges("k") != nil {
		t.Fatal("nil map take must be nil")
	}
	lg.RecordPendingNudge("k", "t")
	if got := lg.TakePendingNudges("k"); len(got) != 1 || got[0] != "t" {
		t.Fatalf("got %v", got)
	}
}

func TestCovResetToolCallHistory(t *testing.T) {
	lg := newTestManager()
	lg.RecordToolCalls("main", []string{"a"}, false)
	lg.RecordToolCalls("main", []string{"a"}, false)
	lg.RecordToolCalls("main", []string{"a"}, false)
	lg.ResetToolCallHistory("")
	lg.ResetToolCallHistory("main")
	if lg.CheckAndResetOscillation("main") {
		t.Fatal("history was reset, no oscillation expected")
	}
}

func TestCovSyncPlanMode(t *testing.T) {
	lg := newTestManager()
	lg.SyncPlanMode("", true)
	lg.SyncPlanMode("auxiliary", true)
	lg.SyncPlanMode("auxiliary|x", true)
	if len(lg.lastPlanMode) != 0 {
		t.Fatal("auxiliary/empty keys must be ignored")
	}
	lg.SyncPlanMode("main", true)
	lg.readWindows["main"] = []int{1, 1}
	lg.nudgeCount["main"] = 1
	lg.SyncPlanMode("main", true) // idempotent while still planning
	if len(lg.readWindows["main"]) != 2 {
		t.Fatal("window must survive while plan mode persists")
	}
	lg.SyncPlanMode("main", false)
	if _, ok := lg.readWindows["main"]; ok {
		t.Fatal("window must clear on true->false")
	}
	if _, ok := lg.nudgeCount["main"]; ok {
		t.Fatal("nudgeCount must clear on true->false")
	}
	if lg.lastPlanMode["main"] {
		t.Fatal("state must record false")
	}
}
