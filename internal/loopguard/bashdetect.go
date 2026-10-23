// Package loopguard is a port of superrelay (v1)'s loopguard.go, split
// across six files by responsibility per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 4.3:
// bashdetect.go (this file — Bash/PowerShell read-vs-write command
// classification), planmode.go (Plan Mode detection + per-agent window/
// threshold selection), manager.go (LoopGuardManager type + sweep +
// RecordEditResult), streaks.go (read-streak recording/checking),
// oscillation.go (repeated-tool-call detection), nudge.go
// (InjectUserNudge). Every regex and heuristic's BODY is unchanged from
// v1 — per Part 2 p.2 of the spec, these are protected only by
// regression tests, not by restructuring. The one real dependency fix:
// v1's hasModifyingToolsInRequest (planmode.go) called server.go's
// package-private isModifyingToolName directly; that's now
// toolkind.IsModifyingToolName in its own leaf package (same reasoning
// as internal/sessionkey/internal/fakeids — three unrelated future
// packages need this classifier, none of them should own it).
package loopguard

import (
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// readEquivalentBashPattern matches a Bash command doing the same "dump
// raw file content" work the Read tool does — sed -n, head, tail, cat,
// seq-driven range loops, dd/cut/fold/nl — as opposed to a genuinely
// targeted search (grep/rg, or awk used as a pattern search), which is
// exactly what NudgeReadLoop itself tells the model to switch to and must
// never count against this same guard, or the nudge's own advice would
// trip it again immediately.
//
// awk is handled separately below, not in this pattern: an audit caught
// that the original version of this check excluded "awk" unconditionally
// (treating it as always a search), which left `awk 'NR>=1 && NR<=120'` —
// functionally identical line-range pagination, not a search —
// completely invisible to the counter. awkNRPattern distinguishes the
// two: an "NR <comparison>" anywhere in the script is pagination; an awk
// call with no NR comparison at all (e.g. `awk '/pattern/'`) is a real
// search and must stay excluded.
// Head/tail now also match the flagless BSD-style form (`head -50 file`,
// `tail +10 file`) and bare `more`/`less`/`pr` pagers — an audit caught
// that the old `-[nc]`-only requirement missed these entirely. `cat` stays
// in the group (bare `cat file` is still a read), but isReadEquivalentBashCommand
// below excludes it specifically when it's writing TO a file (heredoc/
// redirect), not reading one.
//
// A second audit caught that `head\s+(-[nc]|...)` still required SOME
// flag or number right after "head"/"tail" — a genuinely bare `head
// file.go` or `tail file.go` (no flags at all, the single most common way
// to type either command) matched neither alternative and was completely
// invisible to this guard. `cat`/`more`/`less`/`pr` already match bare
// with no such requirement; head/tail now do too, dropping the dangling
// \s+(...) group entirely.
// An audit caught this had NO command-position anchor at all (unlike
// grepInvocationPattern/awkInvocationPattern below, which already require
// `(^|[\s;&|])` before the command name) — a bare `\b` matches the word
// ANYWHERE, so "git diff HEAD~1" matched "head" inside the git ref
// argument, and "gh pr list" matched "pr" as its own second argument,
// both getting misclassified as a read-equivalent command they aren't.
// Anchoring to actual command-start position (string start, or right
// after a real shell separator — NOT bare whitespace, which is just as
// likely to be an argument separator within the SAME command) fixes both:
// "HEAD" and "pr" sit mid-command, never right after `;`/`&`/`|` or at
// position 0, so neither matches; a real `head file.txt` or `ls && head
// file.txt` still does.
// A Round 15 audit caught the separator class below ([;&|], anchored to
// string-start or a real shell boundary) never included \r or \n — a
// multiline bash script (heredoc, or just several commands joined by real
// newlines rather than ;/&&) has a read command legitimately starting a
// LINE that isn't the start of the whole string and isn't preceded by
// ;/&/|, so it silently matched nothing and LoopGuard never saw the read
// at all on any line past the first.
var readEquivalentBashPattern = regexp.MustCompile(`(?i)(?:^|[\r\n;&|])\s*(sed\s+-n|head|tail|cat|more|less|pr|seq|dd\s+bs=|cut\s+-[df]|fold|nl\s+-)\b`)
var awkNRPattern = regexp.MustCompile(`(?i)\bNR\s*[<>=!]`)

// awkInvocationPattern requires "awk" to appear as an actual command
// (start of string or after a shell boundary character), not merely as a
// substring anywhere in the command line — an audit caught the plain
// strings.Contains(lc, "awk") check below used to match ANY command whose
// argument happened to contain "awk", such as reading a file at
// internal/hawk/auth.go or scripts/awk_report.sh, silently excluding that
// read from the loop counter entirely even though no awk was ever invoked.
// A follow-up audit caught that `[\s;&|]` treats bare WHITESPACE as
// equally valid to a real command separator — but a plain space is just
// as likely to be an ordinary argument separator WITHIN the same command
// (`cat awk_report.sh` has "awk" preceded by a space too), so this never
// actually enforced "right after a pipe/semicolon," only "preceded by any
// whitespace at all" — true for nearly every word in a command line.
// RE2 has no lookbehind, so the fix is structural: require the anchor
// character itself to be a real separator immediately before any
// whitespace, not whitespace standing in for one.
var awkInvocationPattern = regexp.MustCompile(`(?i)(?:^|[\r\n;&|])\s*awk\b`)

// catIsWritePattern matches `cat` used to WRITE a file (heredoc into a
// redirect, e.g. `cat << 'EOF' > file.go`, or `cat file1 file2 > out`) —
// an audit caught this counting as a Read when it's actually genuine
// progress (a file write), which should reset the window, not add to it.
//
// A follow-up audit caught that this same confusion isn't unique to
// "cat": `head -n 50 in.txt > out.txt` or `sed -n '1,50p' in.txt >
// out.txt` are just as genuinely a file WRITE (both head and sed also
// match readEquivalentBashPattern as read-equivalent tools, same as cat),
// but only "cat" itself was ever checked here — head/tail/sed-based
// writes still counted as unproductive reads.
// A follow-up audit caught several more genuine-write shapes this missed
// entirely: `echo "..." > file` (redirect, same shape as cat/head/tail/sed
// — added to the same alternation); `sed -i '...' file` (in-place edit,
// no `>` at all — matched separately, no trailing-redirect requirement);
// `tee file` and `touch file` (write via the command's own mere
// invocation, no redirect needed at all either).
// A Round 15 audit caught that [^|]* can never cross a pipe character by
// construction, so a genuine write pipeline like `cat a | sort > b` —
// cat's own output piped through another command before the real
// redirect — never matched at all: there's no literal ">" between "cat"
// and the first "|". The redirect only needs to exist SOMEWHERE after the
// command on the same line, piped through any number of other commands
// first; switch to [^\n]* so it can cross pipes but still stops at a real
// line boundary (multiline scripts are handled line-by-line elsewhere).
var catIsWritePattern = regexp.MustCompile(`(?i)\b(cat|head|tail|sed|echo)\b[^\n]*>|\bsed\s+-i\b|\btee\b|\btouch\b`)

// stderrRedirectPattern matches a file-descriptor-specific redirect used to
// suppress error output — `2>/dev/null`, `2>&1`, `2>$null` (PowerShell) —
// rather than a genuine stdout write. An audit caught that catIsWritePattern
// and powerShellIsWritePattern both just look for any literal ">" in the
// command, with no way to distinguish that from a plain READ command that
// merely silences its stderr (e.g. `cat file.txt 2>/dev/null`, `Get-Content
// file.txt 2>$null`) — RE2 (Go's regexp engine) has no lookbehind to assert
// "not preceded by a digit" directly in the write-pattern regexes
// themselves, so strip this specific shape out of the command first,
// before testing for a real write-redirect.
// An audit caught this only matched a numeric-prefixed redirect
// (2>$null) — PowerShell's `*>$null` (all-streams redirect, suppressing
// stdout AND stderr both) still had its literal ">" visible to
// powerShellIsWritePattern afterward, misclassifying a plain read like
// `Get-Content file.txt *>$null` as a write. The target is PowerShell's
// null device either way, never a real file being written to, so it's
// exactly the same "read with suppressed output" shape as 2>$null.
// A Round 15 audit caught this stripped ANY digit-prefixed redirect,
// including "1>" — explicit stdout redirect (`cmd 1> out.txt` is exactly
// as real a write as `cmd > out.txt`), not an error-suppression one. That
// erased the only ">" in the command before catIsWritePattern/
// powerShellIsWritePattern ever got to look for one, misclassifying a
// genuine write as a plain read. Only fd 2 (stderr) and PowerShell's "*"
// (all-streams) are genuinely about suppressing/redirecting errors, not
// real output.
var stderrRedirectPattern = regexp.MustCompile(`(2|\*)>\S*`)

func stripStderrRedirects(command string) string {
	return stderrRedirectPattern.ReplaceAllString(command, "")
}

// grepInvocationPattern requires grep/rg to appear as an actual COMMAND —
// at the start of the string, or right after a shell command-boundary
// (whitespace, pipe, semicolon, &&/||) — not merely as a substring anywhere
// in the command line. An audit caught that a plain strings.Contains(lc,
// "grep") also matched "grep" sitting inside a file PATH being read, e.g.
// `cat cmd/grep/main.go` or `less grep_test.go`, wrongly treating an
// ordinary file read as "used search" and skipping it from the read-loop
// counter entirely — the exact opposite of what a loop-nudge guard is for.
// A follow-up audit caught that `cat grep.txt` (a top-level, space-
// separated argument — not embedded in a path like cmd/grep/main.go,
// which the boundary check already handled) still matched: the `\s`
// branch treated the ordinary space between "cat" and "grep.txt" as if
// it were a real command separator. Same structural fix as
// awkInvocationPattern above.
var grepInvocationPattern = regexp.MustCompile(`(?i)(?:^|[\r\n;&|])\s*(grep|rg)\b`)

// jqInvocationPattern recognizes jq as an actual command invocation (not
// a path substring), same boundary convention as awk/grep above — jq
// reads and filters a file's content to stdout, the same read-loop shape
// as sed -n/head/tail, but under a tool name those patterns never match.
var jqInvocationPattern = regexp.MustCompile(`(?i)(?:^|[\r\n;&|])\s*jq\b`)

// psSearchInvocationPattern is grepInvocationPattern's PowerShell
// counterpart, covering findstr/grep/rg the same way — select-string is
// intentionally excluded here since it's checked separately with a plain
// substring match (a file path segment named exactly "select-string" is
// implausible enough not to bother with a command-boundary check).
//
// An audit caught "sls" — PowerShell's standard built-in alias for
// Select-String, as common in practice as "grep" itself — was missing
// entirely: `gc file.txt | sls "pattern"` counted as an unproductive read
// instead of a real search, same failure as grep/rg would have had
// without the boundary check. Unlike "select-string", "sls" is short
// enough to plausibly appear as a substring of something else (e.g. a
// Serverless Framework project directory literally named "sls"), so it
// needs the real command-boundary check here, not a bare substring match.
// Same structural anchor fix as grepInvocationPattern above — `Get-Content
// sls.json` has "sls" preceded by an ordinary space (a top-level filename
// argument), which the old `\s` branch wrongly treated as a real command
// separator.
var psSearchInvocationPattern = regexp.MustCompile(`(?i)(?:^|[\r\n;&|])\s*(findstr|grep|rg|sls)\b`)

// statusListingPipePattern, gitLogContentFlag, and psStatusListingPattern live in
// statuscheck.go (along with IsStatusCheckToolCall) to keep bashdetect.go under
// the 400-line architecture ceiling.

func isReadEquivalentBashCommand(command string) bool {
	lc := strings.ToLower(command)
	if grepInvocationPattern.MatchString(command) {
		return false
	}
	if statusListingPipePattern.MatchString(command) && !gitLogContentFlag.MatchString(command) {
		return false
	}
	if awkInvocationPattern.MatchString(command) {
		return awkNRPattern.MatchString(command)
	}
	// python/perl one-liners doing the same slice-the-file trick under a
	// different interpreter — not exhaustive (this is inherently an
	// arms race the audit itself calls out), but covers the concrete
	// pattern actually observed/predicted: readlines()[a:b]-style slicing.
	// An audit caught bare .read() (not just .readlines()/.splitlines())
	// and jq (reads/filters a file's content to stdout, same read-loop
	// shape as grep/sed -n) were both missing from this set.
	//
	// A follow-up audit caught this and isReadEquivalentPowerShellCommand's
	// own one-liner check below had each accumulated a DIFFERENT subset
	// of the same interpreter-invocation patterns (this one never had
	// node's readFileSync/.NET's ReadAllLines/ReadAllText; the PowerShell
	// one never had splitlines/.read()) — but `node -e "..."`/`python -c
	// "..."` work identically regardless of which SHELL invokes them, so
	// a pattern real from one shell is just as real from the other.
	// Shared single list for both.
	// An audit caught that oneLinerReadPattern/jqInvocationPattern
	// returned true unconditionally, with no write-redirect check at
	// all for THEM — catIsWritePattern below only ever matches
	// cat/head/tail/sed/echo/sed-i/tee/touch by name, so it can't catch
	// `jq '.k="v"' file.json > out.json` or `python -c "..." > out.txt`
	// either (verified: catIsWritePattern doesn't match either string).
	// These tools write real output via a plain stdout redirect, not one
	// of those named commands, so check for a bare, non-stderr `>`
	// directly before trusting the read-pattern match.
	if (oneLinerReadPattern.MatchString(lc) || jqInvocationPattern.MatchString(command)) &&
		!strings.Contains(stripStderrRedirects(command), ">") {
		return true
	}
	if catIsWritePattern.MatchString(stripStderrRedirects(command)) {
		return false
	}
	return readEquivalentBashPattern.MatchString(command)
}

// isReadEquivalentPowerShellCommand is readEquivalentBashPattern's
// counterpart for the PowerShell tool — an audit caught that
// readLoopEquivalentToolName only ever recognized "Read" and "Bash" by
// name, leaving every PowerShell call (the primary shell tool on Windows,
// where this gateway's own maintainer runs it) completely invisible to
// the read-loop counter regardless of what it did. Select-String is
// PowerShell's grep equivalent — a genuine targeted search — and must
// stay excluded the same way grep/rg does for Bash.
// cat is a built-in PowerShell alias for Get-Content — an audit caught it
// was missing here, leaving a bare `cat file.txt` completely invisible.
// A Round 15 audit caught that \btype\b (and \bmore\b) here had no
// command-boundary anchor at all, unlike every other read-equivalent
// pattern in this file — \b only asserts a word boundary, not "this is
// the command being invoked", so `git commit -m "fix type error"` or any
// prose/commit-message mentioning the bare word "type" matched and got
// wrongly counted as a read. Anchor cat/get-content/gc/type/more to
// actual command-start position the same way readEquivalentBashPattern
// etc. already do, including the \r\n case for multiline scripts.
var readEquivalentPowerShellPattern = regexp.MustCompile(`(?i)(?:^|[\r\n;&|])\s*(get-content|gc|cat|type|more)\b|select-object\s+-(first|last|skip)`)

// powerShellIsWritePattern is catIsWritePattern's PowerShell counterpart —
// an audit caught that `cat file.txt > out.txt` or `gc file.txt | Out-File
// out.txt` (reading one file specifically to write it elsewhere — genuine
// forward progress) had no exclusion here, miscounting it as an
// unproductive read-loop turn the same way plain Bash `cat` without this
// guard used to.
// An audit caught this only matched the full cmdlet names, not their
// standard short aliases (sc = Set-Content, ac = Add-Content) — a model
// using the alias form was invisible to BOTH this write check and the
// read-equivalent check below, so it counted as neither a read nor a
// write and never reset the read-streak counter after a genuine write.
// A Round 15 audit caught \bsc\b/\bac\b had the same missing-anchor
// problem as \btype\b above — "sc" and "ac" are short enough to
// plausibly appear as an ordinary standalone word/argument (a branch or
// ticket reference like "sc-1234", "ac" as an abbreviation in a commit
// message) with no real command invocation at all. Anchor them to actual
// command-start position; the bare ">" and full cmdlet names stay
// unanchored since a literal redirect character or a full cmdlet name is
// unambiguous evidence on its own.
var powerShellIsWritePattern = regexp.MustCompile(`(?i)>|Out-File|Set-Content|Add-Content|(?:^|[\r\n;&|])\s*(sc|ac)\b`)

func isReadEquivalentPowerShellCommand(command string) bool {
	lc := strings.ToLower(command)
	// select-string is PowerShell's own grep equivalent; findstr/rg/grep are
	// commonly invoked from PowerShell too (findstr is a native Windows
	// tool, rg/grep if Git-for-Windows or similar is on PATH) — an audit
	// caught that only select-string was excluded, so a genuine targeted
	// search piped through any of the others got miscounted as unproductive
	// reading instead of recognized as exactly what the nudge recommends.
	if strings.Contains(lc, "select-string") || psSearchInvocationPattern.MatchString(command) {
		return false
	}
	if psStatusListingPattern.MatchString(command) && !gitLogContentFlag.MatchString(command) {
		return false
	}
	if readEquivalentPowerShellPattern.MatchString(command) {
		if powerShellIsWritePattern.MatchString(stripStderrRedirects(command)) {
			return false
		}
		return true
	}
	// An audit caught that isReadEquivalentBashCommand's own
	// readlines/splitlines check (for a Python/Perl one-liner invoked from
	// Bash) had no PowerShell counterpart — on Windows the exact same
	// dump-the-file one-liners are routinely invoked through `python -c
	// "..."`, `node -e "..."`, or .NET's own [System.IO.File] static
	// methods, all completely invisible to this guard without this. Same
	// shared pattern as the Bash side now uses — see its own comment for
	// why a single combined list, not two different subsets.
	if oneLinerReadPattern.MatchString(lc) {
		return true
	}
	return false
}

// oneLinerReadPattern recognizes an interpreter one-liner (Python/Perl/
// Node/.NET) dumping or slicing a file's content, regardless of which
// SHELL invoked it — `node -e "..."` or `python -c "..."` behave
// identically whether launched from Bash or PowerShell, so both call
// sites share this single list instead of each tracking its own
// incomplete subset.
var oneLinerReadPattern = regexp.MustCompile(`readlines|splitlines|\.read\(|readfilesync|readalllines|readalltext`)

// readLoopEquivalentToolName extracts the tool name from a "name:argsRaw"
// signature (the streaming/non-streaming path's toolCallSignatures) and
// reports whether this call counts as a Read for read-loop purposes —
// either literally the Read tool, or a Bash command paginating through a
// file the same way under a different name. Verified necessary live: an
// agent nudged off Read switched straight to `sed -n 'N,Mp' file` /
// `head`/`tail` chains in Bash and was then completely invisible to a
// counter that only ever matched the literal string "Read".
// isWriteEquivalent reports true when a Bash/PowerShell command's actual
// TEXT performed a file write (cat <<EOF > file, Set-Content, sed -i, ...).
// An audit caught that toolkind.IsModifyingToolName only ever checks
// the TOOL NAME — "Bash"/"PowerShell" are never in its list, since the
// name alone says nothing about what the command inside it did — so a
// write performed entirely through a shell command was neither counted as
// a read (catIsWritePattern/powerShellIsWritePattern already correctly
// exclude it from that) NOR credited as real progress, falling through
// invisibly: the read-streak counter was never reset after a genuine
// successful write, and LoopGuard could still fire a stop-reading nudge
// right after the model just finished writing the very file it had been
// reading.
func readLoopEquivalentToolName(signature string) (name string, isReadEquivalent bool, isWriteEquivalent bool) {
	idx := strings.IndexByte(signature, ':')
	if idx < 0 {
		return signature, signature == "Read", false
	}
	name = signature[:idx]
	// A Round 15 audit caught these were exact, case-sensitive string
	// comparisons against only "Read"/"Bash"/"PowerShell" — a versioned
	// Anthropic built-in tool name (bash_20250124, text_editor_20250728;
	// the translator and streamer both already recognize these elsewhere)
	// or the "pwsh" alias (already mapped to the same "command" arg key as
	// PowerShell) matched none of them, silently exempting those calls
	// from read-loop/write-progress detection entirely.
	lowerName := strings.ToLower(name)
	if lowerName == "read" {
		return name, true, false
	}
	if lowerName == "bash" || strings.HasPrefix(lowerName, "bash_") {
		command := gjson.Get(signature[idx+1:], "command").String()
		if isReadEquivalentBashCommand(command) {
			return name, true, false
		}
		return name, false, catIsWritePattern.MatchString(stripStderrRedirects(command))
	}
	if lowerName == "powershell" || lowerName == "pwsh" {
		command := gjson.Get(signature[idx+1:], "command").String()
		if isReadEquivalentPowerShellCommand(command) {
			return name, true, false
		}
		return name, false, powerShellIsWritePattern.MatchString(stripStderrRedirects(command))
	}
	return name, false, false
}

// ReadLoopEquivalentToolName exports readLoopEquivalentToolName for
// internal/orchestrate, which needs the identical read/write-equivalence
// classification v1's server.go ran inline (once per tool-call signature)
// after both executeStreamingWithWatchdog and executeNonStreaming finish,
// to feed RecordReadTurnWithRealProgress.
func ReadLoopEquivalentToolName(signature string) (name string, isReadEquivalent bool, isWriteEquivalent bool) {
	return readLoopEquivalentToolName(signature)
}
