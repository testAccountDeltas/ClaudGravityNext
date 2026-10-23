// Part of the loopguard package's port from superrelay (v1)'s
// loopguard.go — see bashdetect.go for the split rationale. This file holds
// status-checking and polling tool identification for the oscillation and
// read-loop detectors.
package loopguard

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// statusListingPipePattern matches a command whose job is to list entries
// or report process/VCS/job status, not dump a file's content — ls/dir, ps,
// tasklist, pgrep, jobs, `git status`/`git log`/`git branch`, docker ps. Live
// traffic caught the real gap this closes: a model polling for async subagent
// completion ran `ls -lt .../tasks/ | head -n 3` to see the most recently updated
// task file, and readEquivalentBashPattern's `head\b` alternative matched the
// pipe stage exactly as if it were `cat file | head` — a genuine content dump —
// because the pattern only ever looks at what comes AFTER the pipe, never at what
// produced the input being paginated. Piping a directory listing or a status
// report through head/tail to limit how many lines scroll by is not reading file
// content; it's the Bash equivalent of truncating terminal output, and must not
// count toward the same counter Read-tool pagination does.
// Deliberately excludes `git log -p`/`--patch`/`--stat`, `git show`, and
// `git diff` from the "status" bucket even though some start with "git"
// — those print real file content (diffs), which is exactly the
// dump-equivalent shape this guard exists to catch, so gitLogContentFlag
// below vetoes the exemption when any of those flags is present. RE2
// (Go's regexp engine) has no lookahead, so that veto can't live inside
// this single regex and is checked separately in code.
var statusListingPipePattern = regexp.MustCompile(`(?i)(?:^|[\r\n;&|])\s*(ls|dir|ps|tasklist|pgrep|jobs|git\s+(status|log|branch)|docker\s+ps|curl|wget|ping|sleep|wait|test\s+-[fe]|gh\s+(run|pr|issue))\b`)
var gitLogContentFlag = regexp.MustCompile(`(?i)\bgit\s+log\b.*(-p\b|--patch\b|--stat\b)`)

// psStatusListingPattern is statusListingPipePattern's PowerShell counterpart —
// covers the built-in PowerShell equivalents: gci/get-childitem for ls,
// get-process for ps, get-job for background jobs, tasklist on Windows, and git/docker.
var psStatusListingPattern = regexp.MustCompile(`(?i)(?:^|[\r\n;&|])\s*(ls|dir|gci|get-childitem|get-process|get-job|tasklist|git\s+(status|log|branch)|docker\s+ps|curl|wget|ping|start-sleep|sleep|wait-job|wait-process|test-path|gh\s+(run|pr|issue))\b`)

// IsStatusCheckToolCall reports whether signature represents a status-
// listing, polling, or synchronization call — either a Claude Code
// built-in tool whose purpose is inspecting background/task state
// (ListAgents, TaskList, TaskGet, CronList, ScheduleWakeup, Monitor),
// or a shell command (ls, dir, ps, tasklist, git status/log/branch, docker ps,
// jobs, pgrep, get-process, get-job) executed via Bash or PowerShell.
//
// Live incident: a coordinator agent running 5 parallel background
// subagents polled their completion using Claude Code's native
// ListAgents tool. Each turn emitted ListAgents:{}, which the
// oscillation detector hashed into the exact same signature. Repeating
// 3x in a row triggered period-1 reps-3 oscillation every ~3 turns,
// forcing the model to emit "[ignoring loop detection]" 15+ times over
// ~10 minutes. Status checks repeated while legitimately waiting on
// async work must not feed the oscillation detector.
func IsStatusCheckToolCall(signature string) bool {
	idx := strings.IndexByte(signature, ':')
	name := strings.ToLower(signature)
	var command string
	if idx >= 0 {
		name = strings.ToLower(signature[:idx])
		command = gjson.Get(signature[idx+1:], "command").String()
	}
	switch name {
	case "listagents", "tasklist", "taskget", "taskoutput", "taskwait", "gettask", "cronlist", "schedulewakeup", "monitor":
		return true
	case "bash":
		return statusListingPipePattern.MatchString(command) && !gitLogContentFlag.MatchString(command)
	case "powershell", "pwsh":
		return psStatusListingPattern.MatchString(command) && !gitLogContentFlag.MatchString(command)
	default:
		if strings.HasPrefix(name, "bash_") {
			return statusListingPipePattern.MatchString(command) && !gitLogContentFlag.MatchString(command)
		}
	}
	return false
}
