package loopguard

import (
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func fullManager() *LoopGuardManager {
	m := newTestManager()
	m.pendingNudges = make(map[string][]string)
	return m
}

// seedAll puts state for key into EVERY per-key map and sets lastSeen.
func seedAll(lg *LoopGuardManager, key string, seen time.Time) {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	lg.lastSeen[key] = seen
	lg.readWindows[key] = []int{1, 2}
	lg.toolCallHist[key] = []string{"h"}
	lg.nudgeCount[key] = 2
	lg.editFailures[key] = 3
	lg.lastPlanMode[key] = true
	lg.pendingNudges[key] = []string{"n"}
}

func presentIn(lg *LoopGuardManager, key string) []string {
	lg.mu.Lock()
	defer lg.mu.Unlock()
	var found []string
	if _, ok := lg.lastSeen[key]; ok {
		found = append(found, "lastSeen")
	}
	if _, ok := lg.readWindows[key]; ok {
		found = append(found, "readWindows")
	}
	if _, ok := lg.toolCallHist[key]; ok {
		found = append(found, "toolCallHist")
	}
	if _, ok := lg.nudgeCount[key]; ok {
		found = append(found, "nudgeCount")
	}
	if _, ok := lg.editFailures[key]; ok {
		found = append(found, "editFailures")
	}
	if _, ok := lg.lastPlanMode[key]; ok {
		found = append(found, "lastPlanMode")
	}
	if _, ok := lg.pendingNudges[key]; ok {
		found = append(found, "pendingNudges")
	}
	return found
}

func TestSweepStale_ClearsEveryMapForStaleKeysOnly(t *testing.T) {
	lg := fullManager()
	seedAll(lg, "stale", time.Now().Add(-loopGuardStaleAfter-time.Minute))
	seedAll(lg, "fresh", time.Now().Add(-loopGuardStaleAfter+time.Minute))
	lg.sweepStale()
	if got := presentIn(lg, "stale"); len(got) != 0 {
		t.Fatalf("stale key leaked in %v", got)
	}
	if got := presentIn(lg, "fresh"); len(got) != 7 {
		t.Fatalf("fresh key must be untouched in all 7 maps, found only %v", got)
	}
}

func TestSweepStale_EmptyManagerAndNilPendingMapAreSafe(t *testing.T) {
	fullManager().sweepStale()
	m := newTestManager() // pendingNudges nil
	m.lastSeen["k"] = time.Now().Add(-5 * time.Hour)
	m.sweepStale()
}

func TestPendingNudges_DedupAndTakeClears(t *testing.T) {
	lg := fullManager()
	lg.RecordPendingNudge("s", "one")
	lg.RecordPendingNudge("s", "one") // duplicate text ignored
	lg.RecordPendingNudge("s", "two")
	lg.RecordPendingNudge("other", "x")
	got := lg.TakePendingNudges("s")
	if !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("got %v", got)
	}
	if again := lg.TakePendingNudges("s"); len(again) != 0 {
		t.Fatalf("Take must clear, got %v", again)
	}
	if o := lg.TakePendingNudges("other"); len(o) != 1 {
		t.Fatal("other key must be untouched by taking s")
	}
	lg.RecordPendingNudge("s", "one") // after Take the same text may be recorded again
	if got := lg.TakePendingNudges("s"); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestPendingNudges_EmptyKeyOrTextIgnoredAndNilMapTolerated(t *testing.T) {
	m := newTestManager() // nil pendingNudges
	m.RecordPendingNudge("", "x")
	m.RecordPendingNudge("k", "")
	if m.TakePendingNudges("") != nil || m.TakePendingNudges("k") != nil {
		t.Fatal("nothing should be pending")
	}
	m.RecordPendingNudge("k", "x") // lazily creates the map
	if got := m.TakePendingNudges("k"); len(got) != 1 {
		t.Fatalf("got %v", got)
	}
}

func TestLoopGuard_ConcurrentAccessSameKey(t *testing.T) {
	lg := fullManager()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				lg.RecordPendingNudge("k", fmt.Sprint("n", (i+j)%5))
				lg.RecordEditResult("k", j%3 == 0)
				lg.RecordReadTurn("k", 2, false, ReadWindowSize, false)
				lg.RecordToolCalls("k", []string{fmt.Sprint("c", j%4)}, false)
				_ = lg.CheckAndResetOscillation("k")
				_ = lg.CheckAndResetReadStreak("k", ReadWindowThreshold)
				_ = lg.TakePendingNudges("k")
				lg.sweepStale()
			}
		}(i)
	}
	wg.Wait()
}

func TestOscillation_ABABWithDifferentArgsDoesNotTrigger(t *testing.T) {
	lg := fullManager()
	// Same tool names alternating, but every call carries different args:
	// all hashes are distinct, so there is no period at all.
	for i := 0; i < 12; i++ {
		name := "Read"
		if i%2 == 1 {
			name = "Grep"
		}
		lg.RecordToolCalls("agent", []string{fmt.Sprintf("%s:{\"arg\":%d}", name, i)}, false)
		if lg.CheckAndResetOscillation("agent") {
			t.Fatalf("false positive at turn %d", i)
		}
	}
}

func TestOscillation_ShortABABDoesNotTriggerButABABABDoes(t *testing.T) {
	lg := fullManager()
	feed := func(c string) bool {
		lg.RecordToolCalls("agent", []string{c}, false)
		return lg.CheckAndResetOscillation("agent")
	}
	for _, c := range []string{"A", "B", "A", "B", "A"} {
		if feed(c) {
			t.Fatal("fewer than 3 full A/B periods must not trigger")
		}
	}
	if !feed("B") {
		t.Fatal("A/B/A/B/A/B (identical args) is a real oscillation")
	}
	if feed("A") {
		t.Fatal("history must be reset after a detection")
	}
}

func TestOscillation_IgnoredKeysNeverRecord(t *testing.T) {
	lg := fullManager()
	for _, k := range []string{"", "auxiliary", "auxiliary|x"} {
		for i := 0; i < 5; i++ {
			lg.RecordToolCalls(k, []string{"same"}, false)
		}
		if lg.CheckAndResetOscillation(k) {
			t.Fatalf("key %q must be exempt", k)
		}
	}
}
