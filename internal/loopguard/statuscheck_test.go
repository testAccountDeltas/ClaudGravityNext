// Tests for IsStatusCheckToolCall and status-checking tool exclusions.
package loopguard

import "testing"

func TestIsStatusCheckToolCall_ClaudeCodeBuiltinTools(t *testing.T) {
	statusTools := []string{
		`ListAgents:{}`,
		`ListAgents`,
		`listagents:{"q":""}`,
		`TaskList:{}`,
		`TaskList:{"reason":"checking background task completion"}`,
		`tasklist:{"reason":"wait"}`,
		`TaskGet:{"taskId":"1"}`,
		`taskget:{"taskId":"subagent-4"}`,
		`CronList:{}`,
		`ScheduleWakeup:{"delaySeconds":60,"noop":true,"prompt":"status"}`,
		`Monitor:{}`,
		`monitor:{"until":"done"}`,
		`TaskOutput:{"taskId":"1"}`,
		`taskwait:{"taskId":"1"}`,
		`GetTask:{"taskId":"1"}`,
	}
	for _, sig := range statusTools {
		if !IsStatusCheckToolCall(sig) {
			t.Errorf("expected %q to be recognized as a status-check tool call", sig)
		}
	}
}

func TestIsStatusCheckToolCall_ShellCommands(t *testing.T) {
	statusCommands := []string{
		`Bash:{"command":"ls -lt tasks/ | head -n 3"}`,
		`Bash:{"command":"dir"}`,
		`Bash:{"command":"ps aux | grep subagent"}`,
		`Bash:{"command":"tasklist //FI \"IMAGENAME eq gateway_v2.exe\""}`,
		`Bash:{"command":"pgrep -f gateway"}`,
		`Bash:{"command":"jobs"}`,
		`Bash:{"command":"git status"}`,
		`Bash:{"command":"git branch -a"}`,
		`Bash:{"command":"git log --oneline -5"}`,
		`Bash:{"command":"docker ps"}`,
		`PowerShell:{"command":"Get-Process"}`,
		`PowerShell:{"command":"Get-ChildItem -Path ./tasks"}`,
		`PowerShell:{"command":"gci"}`,
		`PowerShell:{"command":"Get-Job"}`,
		`PowerShell:{"command":"tasklist"}`,
		`pwsh:{"command":"tasklist /fi \"imagename eq node.exe\""}`,
		`Bash:{"command":"sleep 5"}`,
		`Bash:{"command":"wait 1234"}`,
		`Bash:{"command":"gh run list"}`,
		`PowerShell:{"command":"Start-Sleep -Seconds 5"}`,
		`PowerShell:{"command":"Test-Path ./flag.txt"}`,
	}
	for _, sig := range statusCommands {
		if !IsStatusCheckToolCall(sig) {
			t.Errorf("expected %q to be recognized as a status-check tool call", sig)
		}
	}
}

func TestIsStatusCheckToolCall_NonStatusCommands(t *testing.T) {
	nonStatus := []string{
		`Bash:{"command":"cat tasks/agent3.json"}`,
		`Bash:{"command":"git log -p -2"}`,
		`Bash:{"command":"git log --stat -1"}`,
		`Bash:{"command":"go test ./..."}`,
		`PowerShell:{"command":"Get-Content file.txt"}`,
		`Read:{"file_path":"main.go"}`,
		`Edit:{"file_path":"main.go"}`,
		`Write:{"file_path":"main.go"}`,
		`Agent:{"description":"run audit"}`,
		`ToolSearch:{"query":"TaskCreate"}`,
	}
	for _, sig := range nonStatus {
		if IsStatusCheckToolCall(sig) {
			t.Errorf("expected %q to NOT be recognized as a status-check tool call", sig)
		}
	}
}

func TestOscillation_ListAgentsPoll_NeverTriggers(t *testing.T) {
	lg := newTestManager()
	sig := `ListAgents:{}`
	for i := 0; i < 20; i++ {
		var calls []string
		if !IsStatusCheckToolCall(sig) {
			calls = []string{sig}
		}
		lg.RecordToolCalls("agent1", calls, false)
		if lg.CheckAndResetOscillation("agent1") {
			t.Fatalf("oscillation must never trigger on ListAgents poll, fired at iteration %d", i)
		}
	}
}

func TestOscillation_AlternatingStatusChecks_NeverTriggers(t *testing.T) {
	lg := newTestManager()
	sigs := []string{`ListAgents:{}`, `TaskList:{"reason":"check"}`}
	for i := 0; i < 20; i++ {
		sig := sigs[i%len(sigs)]
		var calls []string
		if !IsStatusCheckToolCall(sig) {
			calls = []string{sig}
		}
		lg.RecordToolCalls("agent1", calls, false)
		if lg.CheckAndResetOscillation("agent1") {
			t.Fatalf("oscillation must never trigger on alternating status checks, fired at iteration %d", i)
		}
	}
}

func TestHasIgnoreLoopTag_CaseInsensitive(t *testing.T) {
	cases := []struct {
		input    string
		expected bool
	}{
		{"[ignoring loop detection]", true},
		{"[Ignoring loop detection]", true},
		{"Thinking: [IGNORING LOOP DETECTION] retrying", true},
		{"[ignoring Loop Detection]", true},
		{"random text without tag", false},
	}
	for _, tc := range cases {
		if got := HasIgnoreLoopTag(tc.input); got != tc.expected {
			t.Errorf("HasIgnoreLoopTag(%q) = %v, expected %v", tc.input, got, tc.expected)
		}
	}
}

func TestStripIgnoreLoopTag(t *testing.T) {
	input := "Hello [Ignoring Loop Detection] World"
	expected := "Hello  World"
	if got := StripIgnoreLoopTag(input); got != expected {
		t.Errorf("StripIgnoreLoopTag(%q) = %q, expected %q", input, got, expected)
	}
}

func TestIsStatusCheckToolCall_GitLogVeto(t *testing.T) {
	// git log with -p, --patch, or --stat must NEVER be treated as a status check
	vetoed := []string{
		`Bash:{"command":"git log -p -2"}`,
		`Bash:{"command":"git log --patch -1"}`,
		`Bash:{"command":"git log --stat -5"}`,
		`PowerShell:{"command":"git log -p"}`,
		`pwsh:{"command":"git log --patch"}`,
		`pwsh:{"command":"git log --stat"}`,
		`bash_20241022:{"command":"git log -p"}`,
	}
	for _, sig := range vetoed {
		if IsStatusCheckToolCall(sig) {
			t.Errorf("git log with content flag %q must be vetoed from status-check exemption", sig)
		}
	}

	// Plain git log without content flags IS a status check
	statusLogs := []string{
		`Bash:{"command":"git log --oneline -5"}`,
		`Bash:{"command":"git log -n 10"}`,
		`PowerShell:{"command":"git log --oneline"}`,
		`pwsh:{"command":"git log"}`,
	}
	for _, sig := range statusLogs {
		if !IsStatusCheckToolCall(sig) {
			t.Errorf("plain git log %q should be treated as a status check", sig)
		}
	}
}

func TestRecordReadTurnWithRealProgress_ResetsWindowAndNudge(t *testing.T) {
	lg := newTestManager()
	agent := "agent-progress-test"

	// 1. Accumulate read streak up to threshold
	lg.RecordReadTurnWithRealProgress(agent, 10, false, false, ReadWindowSize, false)
	level := lg.CheckAndResetReadStreak(agent, ReadWindowThreshold)
	if level != 1 {
		t.Fatalf("expected first nudge level 1, got %d", level)
	}

	// 2. Text-only progress (madeProgress=true, realProgress=false)
	// Must reset read window, but preserve nudgeCount=1 so next streak escalates
	lg.RecordReadTurnWithRealProgress(agent, 0, true, false, ReadWindowSize, false)

	// Verify read window is empty
	lg.mu.Lock()
	windowLen := len(lg.readWindows[agent])
	savedNudge := lg.nudgeCount[agent]
	lg.mu.Unlock()
	if windowLen != 0 {
		t.Fatalf("expected read window to be cleared by madeProgress, got len %d", windowLen)
	}
	if savedNudge != 1 {
		t.Fatalf("expected nudgeCount to be preserved on text-only progress, got %d", savedNudge)
	}

	// 3. Accumulate read streak again without real progress -> must escalate to level 2
	lg.RecordReadTurnWithRealProgress(agent, 10, false, false, ReadWindowSize, false)
	level2 := lg.CheckAndResetReadStreak(agent, ReadWindowThreshold)
	if level2 != 2 {
		t.Fatalf("expected escalated nudge level 2, got %d", level2)
	}

	// 4. Genuine Write/Edit progress (madeProgress=true, realProgress=true)
	// Must reset BOTH read window AND nudgeCount back to 0 (clean slate)
	lg.RecordReadTurnWithRealProgress(agent, 0, true, true, ReadWindowSize, false)

	lg.mu.Lock()
	windowLenAfter := len(lg.readWindows[agent])
	nudgeAfter := lg.nudgeCount[agent]
	lg.mu.Unlock()
	if windowLenAfter != 0 {
		t.Fatalf("expected read window cleared on real progress, got len %d", windowLenAfter)
	}
	if nudgeAfter != 0 {
		t.Fatalf("expected nudgeCount cleared on real progress, got %d", nudgeAfter)
	}

	// 5. Subsequent read streak starts from level 1 again
	lg.RecordReadTurnWithRealProgress(agent, 10, false, false, ReadWindowSize, false)
	levelAfterClean := lg.CheckAndResetReadStreak(agent, ReadWindowThreshold)
	if levelAfterClean != 1 {
		t.Fatalf("expected level 1 after real progress clean slate, got %d", levelAfterClean)
	}
}
