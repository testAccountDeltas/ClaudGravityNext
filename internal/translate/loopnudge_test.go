package translate

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"claudgravity/internal/loopguard"
	"claudgravity/internal/protocol/claude"
)

var nudgeSeq atomic.Int64

// nudgeKeys returns a loop key unique to this test; the loop-guard manager is
// a process-wide singleton, so tests must never share keys.
func nudgeKeys(t *testing.T) (agentKey, sessionID, instanceID, loopKey string) {
	t.Helper()
	n := nudgeSeq.Add(1)
	agentKey, sessionID, instanceID = "main", fmt.Sprintf("sess-%d", n), fmt.Sprintf("inst-%d", n)
	return agentKey, sessionID, instanceID, agentKey + "|" + sessionID + "|" + instanceID
}

func mustReq(t *testing.T, js string) claude.Request {
	t.Helper()
	var r claude.Request
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatalf("bad request json: %v", err)
	}
	return r
}

// texts returns every text block of the last message.
func lastTexts(r claude.Request) []string {
	var out []string
	for _, b := range r.Messages[len(r.Messages)-1].Content {
		if tb, ok := b.AsText(); ok {
			out = append(out, tb.Text)
		}
	}
	return out
}

func anyNudge(r claude.Request) bool {
	for _, n := range []string{loopguard.NudgeReadLoop, loopguard.NudgeReadLoopEscalated, loopguard.NudgeOscillation, loopguard.NudgeEditFailureLoop} {
		if hasText(r, n) {
			return true
		}
	}
	return false
}

func hasText(r claude.Request, want string) bool {
	for _, s := range lastTexts(r) {
		if s == want {
			return true
		}
	}
	return false
}

// plain: a user turn that has not made any tool call yet.
const plainReq = `{"model":"m","messages":[{"role":"user","content":"hello"}]}`

// withWrite has an earlier Write call, which makes the main agent's strict
// read-loop limits (10 reads in 15 turns) apply instead of the relaxed ones.
const withWriteReq = `{"model":"m","tools":[{"name":"Write","input_schema":{"type":"object"}}],"messages":[
 {"role":"user","content":"start"},
 {"role":"assistant","content":[{"type":"tool_use","id":"w1","name":"Write","input":{"file_path":"a","content":"x"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"w1","content":"ok"}]}]}`

func fillReadWindow(lg *loopguard.LoopGuardManager, key string, turns int) {
	for i := 0; i < turns; i++ {
		lg.RecordReadTurn(key, 1, false, loopguard.ReadWindowSize, false)
	}
}

func TestNudge_ReadLoopFiresThenEscalates(t *testing.T) {
	agent, sess, inst, key := nudgeKeys(t)
	lg := loopguard.Global()

	req := applyLoopGuardNudges(mustReq(t, withWriteReq), agent, sess, inst)
	if anyNudge(req) {
		t.Fatal("no reads yet, nothing may be injected")
	}

	fillReadWindow(lg, key, loopguard.ReadWindowThreshold-1)
	req = applyLoopGuardNudges(mustReq(t, withWriteReq), agent, sess, inst)
	if anyNudge(req) {
		t.Fatal("one read short of the threshold must not nudge")
	}

	fillReadWindow(lg, key, 1)
	req = applyLoopGuardNudges(mustReq(t, withWriteReq), agent, sess, inst)
	if !hasText(req, loopguard.NudgeReadLoop) {
		t.Fatalf("threshold reached: want the soft read-loop nudge, got %v", lastTexts(req))
	}

	// The window was consumed by the nudge: an immediate repeat must stay quiet.
	if again := applyLoopGuardNudges(mustReq(t, withWriteReq), agent, sess, inst); anyNudge(again) {
		t.Fatal("a spent window must not nudge again")
	}

	// Back into another full read loop without writing: escalated wording.
	fillReadWindow(lg, key, loopguard.ReadWindowThreshold)
	req = applyLoopGuardNudges(mustReq(t, withWriteReq), agent, sess, inst)
	if !hasText(req, loopguard.NudgeReadLoopEscalated) || hasText(req, loopguard.NudgeReadLoop) {
		t.Fatalf("second loop must escalate, got %v", lastTexts(req))
	}
}

func TestNudge_ExploreLimitsAreRelaxedWithoutModifyingTools(t *testing.T) {
	agent, sess, inst, key := nudgeKeys(t)
	fillReadWindow(loopguard.Global(), key, loopguard.ReadWindowThreshold+5)
	req := applyLoopGuardNudges(mustReq(t, plainReq), agent, sess, inst)
	if anyNudge(req) {
		t.Fatal("a read-only session may read far more than 10 files before a nudge")
	}
}

func TestNudge_PlanModeUsesRelaxedLimitsAndExitingResetsTheWindow(t *testing.T) {
	agent, sess, inst, key := nudgeKeys(t)
	lg := loopguard.Global()
	plan := `{"model":"m","tools":[{"name":"ExitPlanMode","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"plan"}]}`
	fillReadWindow(lg, key, loopguard.ReadWindowThreshold+3)
	if got := applyLoopGuardNudges(mustReq(t, plan), agent, sess, inst); anyNudge(got) {
		t.Fatal("plan mode must not nudge at the strict threshold")
	}
	// Leaving plan mode drops the lenient window instead of judging it strictly.
	if got := applyLoopGuardNudges(mustReq(t, withWriteReq), agent, sess, inst); anyNudge(got) {
		t.Fatal("the plan-mode window must not carry into execution")
	}
}

func TestNudge_OscillationFiresOnceAndResetsHistory(t *testing.T) {
	agent, sess, inst, key := nudgeKeys(t)
	lg := loopguard.Global()
	call := []string{"Bash:ls"}
	lg.RecordToolCalls(key, append([]string(nil), call...), false)
	lg.RecordToolCalls(key, append([]string(nil), call...), false)
	if got := applyLoopGuardNudges(mustReq(t, plainReq), agent, sess, inst); anyNudge(got) {
		t.Fatal("two identical calls are not yet a loop")
	}
	lg.RecordToolCalls(key, append([]string(nil), call...), false)
	got := applyLoopGuardNudges(mustReq(t, plainReq), agent, sess, inst)
	if !hasText(got, loopguard.NudgeOscillation) {
		t.Fatalf("three identical turns: want the oscillation nudge, got %v", lastTexts(got))
	}
	if again := applyLoopGuardNudges(mustReq(t, plainReq), agent, sess, inst); anyNudge(again) {
		t.Fatal("history is reset after a nudge; it must not fire again at once")
	}
}

func TestNudge_IgnoreLoopTagClearsOscillationHistory(t *testing.T) {
	agent, sess, inst, key := nudgeKeys(t)
	lg := loopguard.Global()
	for i := 0; i < 3; i++ {
		lg.RecordToolCalls(key, []string{"Bash:ls"}, false)
	}
	tagged := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"keep going [ignoring loop detection]"}]}]}`
	if got := applyLoopGuardNudges(mustReq(t, tagged), agent, sess, inst); hasText(got, loopguard.NudgeOscillation) {
		t.Fatal("the explicit tag must suppress the oscillation nudge")
	}
}

func editTurn(tool, id string, isError bool) string {
	return fmt.Sprintf(`{"model":"m","messages":[
 {"role":"user","content":"fix it"},
 {"role":"assistant","content":[{"type":"tool_use","id":%q,"name":%q,"input":{"file_path":"a","old_string":"x","new_string":"y"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"is_error":%v,"content":"r"}]}]}`, id, tool, id, isError)
}

func TestNudge_EditFailureStreakFiresOnTheThirdFailure(t *testing.T) {
	agent, sess, inst, _ := nudgeKeys(t)
	for i := 1; i <= 3; i++ {
		got := applyLoopGuardNudges(mustReq(t, editTurn("Edit", fmt.Sprintf("e%d", i), true)), agent, sess, inst)
		fired := hasText(got, loopguard.NudgeEditFailureLoop)
		if fired != (i == 3) {
			t.Fatalf("failure #%d: nudge fired=%v, want %v", i, fired, i == 3)
		}
	}
	// Still failing: keeps nudging, it does not go quiet after the first time.
	if got := applyLoopGuardNudges(mustReq(t, editTurn("Edit", "e4", true)), agent, sess, inst); !hasText(got, loopguard.NudgeEditFailureLoop) {
		t.Fatal("a 4th consecutive failure must still carry the nudge")
	}
}

func TestNudge_EditSuccessResetsTheStreak(t *testing.T) {
	agent, sess, inst, _ := nudgeKeys(t)
	applyLoopGuardNudges(mustReq(t, editTurn("Edit", "a", true)), agent, sess, inst)
	applyLoopGuardNudges(mustReq(t, editTurn("Edit", "b", true)), agent, sess, inst)
	applyLoopGuardNudges(mustReq(t, editTurn("Edit", "c", false)), agent, sess, inst) // success resets
	got := applyLoopGuardNudges(mustReq(t, editTurn("Edit", "d", true)), agent, sess, inst)
	if hasText(got, loopguard.NudgeEditFailureLoop) {
		t.Fatal("a success in between must restart the count")
	}
}

func TestNudge_NonEditTurnDoesNotResetTheStreak(t *testing.T) {
	agent, sess, inst, _ := nudgeKeys(t)
	applyLoopGuardNudges(mustReq(t, editTurn("Edit", "a", true)), agent, sess, inst)
	applyLoopGuardNudges(mustReq(t, editTurn("Edit", "b", true)), agent, sess, inst)
	readTurn := `{"model":"m","messages":[
 {"role":"user","content":"x"},
 {"role":"assistant","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":"a"}}]},
 {"role":"user","content":[{"type":"tool_result","tool_use_id":"r1","content":"file"}]}]}`
	applyLoopGuardNudges(mustReq(t, readTurn), agent, sess, inst) // model re-reads the file
	got := applyLoopGuardNudges(mustReq(t, editTurn("Edit", "c", true)), agent, sess, inst)
	if !hasText(got, loopguard.NudgeEditFailureLoop) {
		t.Fatal("re-reading the file between failed edits must not forgive the streak")
	}
}

func TestNudge_UndeliverableNudgeIsQueuedForTheSystemPrompt(t *testing.T) {
	agent, sess, inst, key := nudgeKeys(t)
	for i := 0; i < 3; i++ {
		lg := loopguard.Global()
		lg.RecordToolCalls(key, []string{"Bash:ls"}, false)
	}
	// The conversation ends with an assistant turn, so nothing can be appended
	// to a user message; the notice must wait for the system prompt instead.
	assistantTail := `{"model":"m","messages":[{"role":"user","content":"x"},{"role":"assistant","content":[{"type":"text","text":"y"}]}]}`
	got := applyLoopGuardNudges(mustReq(t, assistantTail), agent, sess, inst)
	if n := len(got.Messages[len(got.Messages)-1].Content); n != 1 {
		t.Fatalf("the assistant turn must be left alone, content blocks = %d", n)
	}
	pending := loopguard.TakePendingNudges(key)
	if len(pending) != 1 || pending[0] != loopguard.NudgeOscillation {
		t.Fatalf("pending = %v, want the oscillation notice", pending)
	}
	if again := loopguard.TakePendingNudges(key); len(again) != 0 {
		t.Fatal("pending notices must be handed out once")
	}
}

func TestInjectNudgeIntoTail(t *testing.T) {
	if _, ok := injectNudgeIntoTail(claude.Request{}, "n"); ok {
		t.Fatal("an empty conversation has no tail to append to")
	}
	if r, ok := injectNudgeIntoTail(mustReq(t, `{"messages":[{"role":"assistant","content":[{"type":"text","text":"a"}]}]}`), "n"); ok || len(r.Messages[0].Content) != 1 {
		t.Fatal("an assistant tail must be left untouched")
	}
	r, ok := injectNudgeIntoTail(mustReq(t, `{"messages":[{"role":"user","content":[{"type":"text","text":"q"}]}]}`), "NOTE")
	if !ok || len(r.Messages[0].Content) != 2 {
		t.Fatalf("ok=%v blocks=%d", ok, len(r.Messages[0].Content))
	}
	tb, isText := r.Messages[0].Content[1].AsText()
	if !isText || tb.Text != "NOTE" || !strings.Contains(string(r.Messages[0].Content[1].Raw), `"type":"text"`) {
		t.Fatalf("appended block = %+v raw=%s", tb, r.Messages[0].Content[1].Raw)
	}
}

func TestLastTurnHadFailedEdit(t *testing.T) {
	multi := func(results string) string {
		return `{"messages":[{"role":"user","content":"x"},{"role":"assistant","content":[
 {"type":"tool_use","id":"e1","name":"Edit","input":{}},{"type":"tool_use","id":"e2","name":"Edit","input":{}}]},
 {"role":"user","content":[` + results + `]}]}`
	}
	res := func(id string, isErr bool) string {
		return fmt.Sprintf(`{"type":"tool_result","tool_use_id":%q,"is_error":%v,"content":"c"}`, id, isErr)
	}
	for _, c := range []struct {
		name         string
		js           string
		hadEdit, bad bool
	}{
		{"edit failed", editTurn("Edit", "a", true), true, true},
		{"edit ok", editTurn("Edit", "a", false), true, false},
		{"alias str_replace_editor", editTurn("str_replace_editor", "a", true), true, true},
		{"alias update, any case", editTurn("UPDATE", "a", true), true, true},
		{"notebook edit", editTurn("NotebookEdit", "a", true), true, true},
		{"Write is not an edit", editTurn("Write", "a", true), false, false},
		{"all edits failed", multi(res("e1", true) + "," + res("e2", true)), true, true},
		{"batch with one success is forward progress", multi(res("e1", true) + "," + res("e2", false)), true, false},
		{"results for other ids are ignored", multi(res("zz", true)), false, false},
		{"single message", `{"messages":[{"role":"user","content":"x"}]}`, false, false},
		{"tail is assistant", `{"messages":[{"role":"user","content":"x"},{"role":"assistant","content":"y"}]}`, false, false},
		{"previous is not assistant", `{"messages":[{"role":"user","content":"x"},{"role":"user","content":"y"}]}`, false, false},
		{"a tool_use in a user turn is not an edit by the model", `{"messages":[{"role":"user","content":[{"type":"tool_use","id":"u1","name":"Edit","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"u1","is_error":true,"content":"c"}]}]}`, false, false},
	} {
		had, bad := lastTurnHadFailedEdit(mustReq(t, c.js))
		if had != c.hadEdit || bad != c.bad {
			t.Errorf("%s: got (had=%v failed=%v), want (%v, %v)", c.name, had, bad, c.hadEdit, c.bad)
		}
	}
}
