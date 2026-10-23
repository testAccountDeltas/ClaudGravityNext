// Phase 1 characterization test for the ported loopguard package —
// covers the bash/powershell read-vs-write classification, read-streak
// threshold logic, oscillation detection, and InjectUserNudge's
// role-alternation/tool_result-folding behavior, all unchanged from v1.
package loopguard

import (
	"testing"
	"time"
)

func TestIsReadEquivalentBashCommand_GrepIsSearchNotRead(t *testing.T) {
	if isReadEquivalentBashCommand("grep -rn TODO .") {
		t.Fatal("grep is a genuine search, must never count as a read-equivalent")
	}
}

func TestIsReadEquivalentBashCommand_AwkWithNRIsPagination(t *testing.T) {
	if !isReadEquivalentBashCommand("awk 'NR>=1 && NR<=120' file.go") {
		t.Fatal("awk with an NR comparison is line-range pagination, same as head/tail")
	}
	if isReadEquivalentBashCommand("awk '/pattern/' file.go") {
		t.Fatal("awk with no NR comparison is a real search, must not count as read-equivalent")
	}
}

func TestIsReadEquivalentBashCommand_BareHeadTailHandled(t *testing.T) {
	if !isReadEquivalentBashCommand("head file.go") || !isReadEquivalentBashCommand("tail file.go") {
		t.Fatal("bare head/tail with no flags at all must still count as read-equivalent")
	}
}

func TestIsReadEquivalentBashCommand_CommandPositionAnchored(t *testing.T) {
	// "head"/"pr" appearing mid-argument (not as the actual command) must
	// not match — this is the exact git/gh false-positive an audit caught.
	if isReadEquivalentBashCommand("git diff HEAD~1") {
		t.Fatal("HEAD inside a git ref argument must not match 'head' as a command")
	}
	if isReadEquivalentBashCommand("gh pr list") {
		t.Fatal("'pr' as gh's own subcommand argument must not match the 'pr' pager command")
	}
}

func TestIsReadEquivalentBashCommand_StatusListingPipedThroughHeadIsNotRead(t *testing.T) {
	// Live incident: a model polling for async subagent completion ran
	// `ls -lt .../tasks/ | head -n 3` — readEquivalentBashPattern's bare
	// "head" alternative matched the pipe stage as if it were a genuine
	// `cat file | head` content dump, miscounting directory-listing
	// pagination as unproductive file reading.
	cases := []string{
		`ls -lt C:/Users/PC/AppData/Local/Temp/claude/tasks/ | head -n 3`,
		"ps aux | head",
		"git status | head -5",
		"git log --oneline | tail -10",
		"git branch -a | head",
		"docker ps | head",
	}
	for _, c := range cases {
		if isReadEquivalentBashCommand(c) {
			t.Fatalf("%q is listing/status output pagination, must not count as read-equivalent", c)
		}
	}
}

func TestIsReadEquivalentBashCommand_GitLogContentFlagStillCountsAsRead(t *testing.T) {
	// git log -p/--patch/--stat prints real file content (diffs), unlike
	// plain git log/status/branch — must stay a genuine read-equivalent
	// despite starting with "git log".
	if !isReadEquivalentBashCommand("git log -p file.go | head -100") {
		t.Fatal("git log -p dumps real diff content and must still count as read-equivalent")
	}
}

func TestIsReadEquivalentBashCommand_WriteRedirectExcluded(t *testing.T) {
	if isReadEquivalentBashCommand("cat file.txt > out.txt") {
		t.Fatal("cat piped to a real write redirect is progress, not a read-loop turn")
	}
	if isReadEquivalentBashCommand("cat a | sort > b") {
		t.Fatal("a write redirect reached through a pipe chain must still be recognized as a write")
	}
}

func TestIsReadEquivalentBashCommand_StderrSuppressionStillCountsAsRead(t *testing.T) {
	if !isReadEquivalentBashCommand("cat file.txt 2>/dev/null") {
		t.Fatal("suppressing stderr doesn't change a plain read into a write")
	}
}

func TestIsReadEquivalentBashCommand_PythonOneLinerSlice(t *testing.T) {
	if !isReadEquivalentBashCommand(`python -c "print(open('f').readlines()[10:50])"`) {
		t.Fatal("a Python one-liner slicing a file's lines is the same pagination pattern as sed -n/head/tail")
	}
}

func TestIsReadEquivalentPowerShellCommand_SelectStringIsSearch(t *testing.T) {
	if isReadEquivalentPowerShellCommand(`Select-String -Pattern "TODO" -Path *.go`) {
		t.Fatal("Select-String is PowerShell's grep equivalent, must not count as read-equivalent")
	}
	if isReadEquivalentPowerShellCommand(`gc file.txt | sls "pattern"`) {
		t.Fatal("sls (Select-String alias) piped from Get-Content is still a real search")
	}
}

func TestIsReadEquivalentPowerShellCommand_GetContentWithWriteExcluded(t *testing.T) {
	if isReadEquivalentPowerShellCommand(`gc file.txt | Out-File out.txt`) {
		t.Fatal("Get-Content piped to Out-File is a write, not an unproductive read")
	}
}

func TestIsReadEquivalentPowerShellCommand_BareCat(t *testing.T) {
	if !isReadEquivalentPowerShellCommand("cat file.txt") {
		t.Fatal("bare cat (PowerShell alias for Get-Content) must count as read-equivalent")
	}
}

func TestIsReadEquivalentPowerShellCommand_StatusListingPipedThroughSelectObjectIsNotRead(t *testing.T) {
	// PowerShell analog of the ls|head live incident: Get-ChildItem's
	// output limited via Select-Object -First/-Last is a directory
	// listing, not file content — but readEquivalentPowerShellPattern's
	// own `select-object\s+-(first|last|skip)` alternative has no
	// left-hand context and matched regardless of what produced the
	// input being paginated.
	cases := []string{
		"Get-ChildItem tasks | Select-Object -First 3",
		"gci | select -Last 5",
		"ls | Select-Object -First 1",
		"git status | Select-Object -First 5",
	}
	for _, c := range cases {
		if isReadEquivalentPowerShellCommand(c) {
			t.Fatalf("%q is listing output pagination, must not count as read-equivalent", c)
		}
	}
}

func TestReadLoopEquivalentToolName_ReadTool(t *testing.T) {
	name, isRead, isWrite := readLoopEquivalentToolName("Read")
	if name != "Read" || !isRead || isWrite {
		t.Fatalf("got name=%q isRead=%v isWrite=%v", name, isRead, isWrite)
	}
}

func TestReadLoopEquivalentToolName_BashReadVsWrite(t *testing.T) {
	_, isRead, _ := readLoopEquivalentToolName(`Bash:{"command":"head file.go"}`)
	if !isRead {
		t.Fatal("expected a Bash head command to classify as read-equivalent")
	}
	_, isRead2, isWrite2 := readLoopEquivalentToolName(`Bash:{"command":"cat file.go > out.go"}`)
	if isRead2 || !isWrite2 {
		t.Fatalf("expected a Bash write-redirect command to classify as write, got isRead=%v isWrite=%v", isRead2, isWrite2)
	}
}

func TestReadLoopEquivalentToolName_VersionedBashToolName(t *testing.T) {
	_, isRead, _ := readLoopEquivalentToolName(`bash_20250124:{"command":"head file.go"}`)
	if !isRead {
		t.Fatal("a versioned Anthropic built-in tool name (bash_20250124) must still be recognized as Bash")
	}
}

func TestReadLoopLimits_SubagentGetsRelaxedLimits(t *testing.T) {
	w, th := readLoopLimits("sub:Explore", true)
	if w != exploreReadWindowSize || th != exploreReadWindowThreshold {
		t.Fatalf("expected relaxed explore limits for a subagent, got window=%d threshold=%d", w, th)
	}
}

func TestReadLoopLimits_MainAgentWithModifyingToolsGetsStrictLimits(t *testing.T) {
	w, th := readLoopLimits("main", true)
	if w != ReadWindowSize || th != ReadWindowThreshold {
		t.Fatalf("expected strict main-agent limits, got window=%d threshold=%d", w, th)
	}
}

func TestReadLoopLimits_NoModifyingToolsAlwaysRelaxed(t *testing.T) {
	w, th := readLoopLimits("main", false)
	if w != exploreReadWindowSize || th != exploreReadWindowThreshold {
		t.Fatalf("expected relaxed limits when no modifying tools are declared, got window=%d threshold=%d", w, th)
	}
}

func newTestManager() *LoopGuardManager {
	return &LoopGuardManager{
		readWindows:  make(map[string][]int),
		toolCallHist: make(map[string][]string),
		lastSeen:     make(map[string]time.Time),
		nudgeCount:   make(map[string]int),
		editFailures: make(map[string]int),
		lastPlanMode: make(map[string]bool),
	}
}

func TestReadStreak_AccumulatesAndFiresAtThreshold(t *testing.T) {
	lg := newTestManager()
	agent := "main"
	for i := 0; i < 5; i++ {
		lg.RecordReadTurn(agent, 3, false, ReadWindowSize, false) // 5*3=15 >= threshold 10
	}
	level := lg.CheckAndResetReadStreak(agent, ReadWindowThreshold)
	if level != 1 {
		t.Fatalf("expected the first nudge to fire at escalation level 1, got %d", level)
	}
	// Window was cleared by the fire above; checking again immediately
	// must not fire again.
	if lg.CheckAndResetReadStreak(agent, ReadWindowThreshold) != 0 {
		t.Fatal("expected no nudge immediately after the window was cleared")
	}
}

func TestReadStreak_MadeProgressClearsWindow(t *testing.T) {
	lg := newTestManager()
	agent := "main"
	lg.RecordReadTurn(agent, 20, false, ReadWindowSize, false) // way over threshold
	lg.RecordReadTurn(agent, 0, true, ReadWindowSize, false)   // genuine progress (Write/Edit)
	if level := lg.CheckAndResetReadStreak(agent, ReadWindowThreshold); level != 0 {
		t.Fatalf("expected madeProgress to clear the window entirely, got nudge level %d", level)
	}
}

func TestReadStreak_EscalatesOnSecondLoopWithoutProgress(t *testing.T) {
	lg := newTestManager()
	agent := "main"
	lg.RecordReadTurn(agent, 15, false, ReadWindowSize, false)
	if lg.CheckAndResetReadStreak(agent, ReadWindowThreshold) != 1 {
		t.Fatal("expected first nudge at level 1")
	}
	// Slides right back into another full read-loop window without any
	// real progress in between — must escalate to level 2, not repeat 1.
	lg.RecordReadTurn(agent, 15, false, ReadWindowSize, false)
	if level := lg.CheckAndResetReadStreak(agent, ReadWindowThreshold); level != 2 {
		t.Fatalf("expected escalation to level 2, got %d", level)
	}
}

func TestOscillation_DetectsRepeatedSingleCall(t *testing.T) {
	lg := newTestManager()
	agent := "main"
	for i := 0; i < 3; i++ {
		lg.RecordToolCalls(agent, []string{"Read:f.go"}, false)
	}
	if !lg.CheckAndResetOscillation(agent) {
		t.Fatal("expected the same single call repeated 3 times to be detected as oscillation")
	}
}

func TestOscillation_IgnoreTagClearsHistory(t *testing.T) {
	lg := newTestManager()
	agent := "main"
	for i := 0; i < 3; i++ {
		lg.RecordToolCalls(agent, []string{"Read:f.go"}, false)
	}
	lg.RecordToolCalls(agent, nil, true) // ignore tag present
	if lg.CheckAndResetOscillation(agent) {
		t.Fatal("the ignore tag must clear history, so no oscillation should be detected after it")
	}
}

func TestRecordEditResult_TracksConsecutiveFailures(t *testing.T) {
	lg := newTestManager()
	agent := "main"
	if n := lg.RecordEditResult(agent, true); n != 1 {
		t.Fatalf("expected count 1, got %d", n)
	}
	if n := lg.RecordEditResult(agent, true); n != 2 {
		t.Fatalf("expected count 2, got %d", n)
	}
	if n := lg.RecordEditResult(agent, false); n != 0 {
		t.Fatalf("a success must reset the count to 0, got %d", n)
	}
}

func TestRecordEditResult_TouchesLastSeen(t *testing.T) {
	lg := newTestManager()
	agent := "agent-edit-test"
	lg.RecordEditResult(agent, true)
	lg.mu.Lock()
	seen, ok := lg.lastSeen[agent]
	lg.mu.Unlock()
	if !ok || seen.IsZero() {
		t.Fatalf("expected lastSeen to be recorded on RecordEditResult")
	}
}

func TestSweepStale_ClearsLastPlanMode(t *testing.T) {
	lg := newTestManager()
	agent := "agent-stale"
	lg.mu.Lock()
	lg.lastSeen[agent] = time.Now().Add(-3 * time.Hour)
	lg.lastPlanMode[agent] = true
	lg.editFailures[agent] = 5
	lg.mu.Unlock()

	lg.sweepStale()

	lg.mu.Lock()
	defer lg.mu.Unlock()
	if _, ok := lg.lastPlanMode[agent]; ok {
		t.Fatalf("expected lastPlanMode to be swept for stale agent")
	}
	if _, ok := lg.editFailures[agent]; ok {
		t.Fatalf("expected editFailures to be swept for stale agent")
	}
}

func TestInjectUserNudge_AppendsToTrailingToolResult(t *testing.T) {
	req := map[string]interface{}{
		"session_id": "sess-test-tool-result",
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": []interface{}{
					map[string]interface{}{"type": "tool_result", "tool_use_id": "t1", "content": `{"status":"ok"}`},
				},
			},
		},
	}
	InjectUserNudge(req, "test nudge")
	msgs := req["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatalf("must keep single message turn, not add a new message, got %d messages", len(msgs))
	}
	// Verify tool_result content is NOT polluted with notice
	tr := msgs[0].(map[string]interface{})["content"].([]interface{})[0].(map[string]interface{})
	if tr["content"] != `{"status":"ok"}` {
		t.Fatalf("tool_result content was corrupted: %v", tr["content"])
	}
	// Verify pending nudge was recorded instead
	nudges := TakePendingNudges("sess-test-tool-result")
	if len(nudges) != 1 || nudges[0] != "test nudge" {
		t.Fatalf("expected pending nudge to be recorded, got %v", nudges)
	}
}

func TestInjectUserNudge_SkipsWhenLastTurnIsAssistant(t *testing.T) {
	req := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{"role": "assistant", "content": []interface{}{
				map[string]interface{}{"type": "text", "text": "partial answer"},
			}},
		},
	}
	InjectUserNudge(req, "test nudge")
	msgs := req["messages"].([]interface{})
	if len(msgs) != 1 {
		t.Fatal("must not append a new message when the conversation ends on an assistant turn (would break prefill)")
	}
}

func TestIsPlanModeRequest_DetectsExitPlanModeTool(t *testing.T) {
	body := []byte(`{"tools":[{"name":"ExitPlanMode"}]}`)
	if !isPlanModeRequest(body) {
		t.Fatal("expected Plan Mode to be detected from the ExitPlanMode tool declaration")
	}
}

func TestHasModifyingToolsInRequest_FalseDuringPlanMode(t *testing.T) {
	body := []byte(`{"tools":[{"name":"ExitPlanMode"},{"name":"Edit"}]}`)
	if hasModifyingToolsInRequest(body) {
		t.Fatal("Plan Mode must report no modifying tools regardless of what's declared alongside ExitPlanMode")
	}
}

// GAP-P1-01: Claude Code declares Edit/Write on essentially every
// session (including pure read-only audits), so the DECLARATION alone
// must not be enough to flip this to true — otherwise the relaxed
// exploratory read-loop threshold would never apply to a real audit
// session, exactly the false-positive pattern an audit caught live.
func TestHasModifyingToolsInRequest_FalseWhenDeclaredButNeverCalled(t *testing.T) {
	body := []byte(`{"tools":[{"name":"Edit"}],"messages":[{"role":"user","content":[{"type":"text","text":"review this file"}]}]}`)
	if hasModifyingToolsInRequest(body) {
		t.Fatal("expected Edit declared-but-never-invoked to report hasModifyingTools=false (relaxed exploratory threshold)")
	}
}

func TestHasModifyingToolsInRequest_TrueOncePreviouslyInvoked(t *testing.T) {
	body := []byte(`{"tools":[{"name":"Edit"}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Edit","input":{}}]}]}`)
	if !hasModifyingToolsInRequest(body) {
		t.Fatal("expected a real past Edit tool_use call to report hasModifyingTools=true outside Plan Mode")
	}
}

// A live incident: a coordinator agent polling the same status-listing
// bash command every turn while waiting on async subagents tripped
// CheckAndResetOscillation's "same call repeated 3x" pattern every ~3
// turns, forcing the model to splice a visible "[ignoring loop
// detection]" tag into its own output over and over for the length of
// a long wait. IsStatusCheckToolCall gives internal/orchestrate the
// same exemption the read-streak counter already had.
func TestIsStatusCheckToolCall_RecognizesStatusListingBash(t *testing.T) {
	sig := `Bash:{"command":"ls -lt tasks/ | head -n 3"}`
	if !IsStatusCheckToolCall(sig) {
		t.Fatalf("expected a status-listing bash command to be recognized, got false for %q", sig)
	}
}

func TestIsStatusCheckToolCall_FalseForRealContentRead(t *testing.T) {
	sig := `Bash:{"command":"cat tasks/agent3.json"}`
	if IsStatusCheckToolCall(sig) {
		t.Fatalf("expected a real content read to NOT be classified as a status check, got true for %q", sig)
	}
}

// GAP: oscillation detection must not fire on a status-check poll
// repeated identically every turn — the same pattern the read-streak
// counter already exempts.
func TestOscillation_StatusCheckCallsFilteredOut_NeverTriggers(t *testing.T) {
	lg := newTestManager()
	sig := `Bash:{"command":"ls -lt tasks/ | head -n 3"}`
	for i := 0; i < 10; i++ {
		if IsStatusCheckToolCall(sig) {
			// Mirrors internal/orchestrate's own filter: a status-check
			// call is never passed to RecordToolCalls at all.
			lg.RecordToolCalls("agent1", nil, false)
		} else {
			lg.RecordToolCalls("agent1", []string{sig}, false)
		}
		if lg.CheckAndResetOscillation("agent1") {
			t.Fatalf("oscillation must never trigger on a filtered-out status-check call, fired at iteration %d", i)
		}
	}
}
