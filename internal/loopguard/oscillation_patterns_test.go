package loopguard

import (
	"fmt"
	"testing"
)

// feed records one tool-call turn per entry; each entry is the set of calls of that turn.
func feed(lg *LoopGuardManager, agent string, turns ...string) {
	for _, c := range turns {
		lg.RecordToolCalls(agent, []string{c}, false)
	}
}

func TestOscillation_PatternsAndNonPatterns(t *testing.T) {
	for _, c := range []struct {
		name  string
		turns []string
		want  bool
	}{
		{"A x2 is not yet a loop", []string{"A", "A"}, false},
		{"A x3", []string{"A", "A", "A"}, true},
		{"A,B x2 is not yet a loop", []string{"A", "B", "A", "B"}, false},
		{"A,B x3", []string{"A", "B", "A", "B", "A", "B"}, true},
		{"A,B,C x2 is not yet a loop", []string{"A", "B", "C", "A", "B"}, false},
		{"A,B,C x2 complete", []string{"A", "B", "C", "A", "B", "C"}, true},
		{"only the most recent window counts", []string{"X", "Y", "Z", "A", "A", "A"}, true},
		{"progress in between breaks the run", []string{"A", "A", "B", "A"}, false},
		{"all different", []string{"A", "B", "C", "D", "E", "F"}, false},
		{"a loop followed by other calls is not current", []string{"A", "A", "A", "B", "C"}, false},
	} {
		lg := newTestManager()
		feed(lg, "main", c.turns...)
		if got := lg.CheckAndResetOscillation("main"); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOscillation_DegenerateTwoCyclesAreNotAPeriodTwoLoop(t *testing.T) {
	// A,A,A,A,A,B has no genuine A/B alternation; only the period-1 rule
	// applies, and it needs the LAST three turns to match.
	lg := newTestManager()
	feed(lg, "main", "A", "A", "A", "A", "A", "B")
	if lg.CheckAndResetOscillation("main") {
		t.Fatal("a run of A's that ended with B is not a current loop")
	}
}

func TestOscillation_CallOrderInsideATurnDoesNotMatter(t *testing.T) {
	lg := newTestManager()
	for i := 0; i < 3; i++ {
		calls := []string{"Read:a", "Read:b"}
		if i%2 == 1 {
			calls = []string{"Read:b", "Read:a"}
		}
		lg.RecordToolCalls("main", calls, false)
	}
	if !lg.CheckAndResetOscillation("main") {
		t.Fatal("the same set of parallel calls in a different order is the same turn")
	}
}

func TestOscillation_FiresOnceThenHistoryIsClear(t *testing.T) {
	lg := newTestManager()
	feed(lg, "main", "A", "A", "A")
	if !lg.CheckAndResetOscillation("main") || lg.CheckAndResetOscillation("main") {
		t.Fatal("a detection must clear history so it fires exactly once")
	}
}

func TestOscillation_HistoryIsCapped(t *testing.T) {
	lg := newTestManager()
	for i := 0; i < ToolCallHistMax*3; i++ {
		feed(lg, "main", fmt.Sprintf("call-%d", i)) // never repeats
	}
	lg.mu.Lock()
	n := len(lg.toolCallHist["main"])
	lg.mu.Unlock()
	if n != ToolCallHistMax {
		t.Fatalf("history length = %d, want the cap %d", n, ToolCallHistMax)
	}
	// And the cap keeps the NEWEST entries: a loop at the end is still seen.
	feed(lg, "main", "L", "L", "L")
	if !lg.CheckAndResetOscillation("main") {
		t.Fatal("a loop at the end of a long history must be detected")
	}
}

func TestOscillation_AgentsAreIndependentAndAuxiliaryIsIgnored(t *testing.T) {
	lg := newTestManager()
	feed(lg, "a", "A", "A", "A")
	if lg.CheckAndResetOscillation("b") {
		t.Fatal("another agent's calls must not count")
	}
	feed(lg, "auxiliary", "A", "A", "A")
	feed(lg, "auxiliary|x", "A", "A", "A")
	if lg.CheckAndResetOscillation("auxiliary") || lg.CheckAndResetOscillation("auxiliary|x") || lg.CheckAndResetOscillation("") {
		t.Fatal("auxiliary and empty keys are never tracked")
	}
	if !lg.CheckAndResetOscillation("a") {
		t.Fatal("agent a's own loop must still be detected")
	}
}

func TestIsReadEquivalentPowerShellCommand_InterpreterOneLinerCountsAsRead(t *testing.T) {
	for _, cmd := range []string{
		`python -c "print(open('f.txt').readlines())"`,
		`node -e "console.log(require('fs').readFileSync('f'))"`,
		`[System.IO.File]::ReadAllText('f.txt')`,
	} {
		if !isReadEquivalentPowerShellCommand(cmd) {
			t.Errorf("%q dumps a file and must count as a read", cmd)
		}
	}
	if isReadEquivalentPowerShellCommand(`Get-Date`) {
		t.Error("an unrelated command is not a read")
	}
}
