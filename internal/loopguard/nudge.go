// Part of the loopguard package's port from superrelay (v1)'s
// loopguard.go — see bashdetect.go for the split rationale. This file
// holds the nudge text constants and InjectUserNudge, which splices a
// synthetic user message into the conversation tail.
package loopguard

// Recommends Bash (grep/rg), not the native Grep tool — strip_search_tools
// defaults to true, so Grep/Glob usually aren't even declared to the
// model; telling it to use a tool it doesn't have is useless advice at
// best and confusing at worst.
const NudgeReadLoop = "Notice from the gateway: you have made many Read calls recently without writing/editing anything or giving a substantial answer — this looks like an unproductive read-loop. Stop reading slices. Use Bash (grep/rg) to jump straight to the relevant symbol/section, or read larger ranges, or proceed with what you have."

const NudgeOscillation = "Notice from the gateway: your output is flagged for looping content (the exact same tool call or A/B oscillation repeated). If intentional, add the tag [ignoring loop detection]; otherwise stop and try a different approach."

// readToolGuidance is appended to the Read tool's own declared
// "description" field (in the translator, where Claude's tool
// declarations get translated into Gemini functionDeclarations) —
// not the system prompt. Two earlier versions lived in the system
// prompt instead and both measurably helped (fixed ~120-line pages
// became variable 40-150-line pages, then a direct test got a
// correctly-reasoned no-offset/limit call) without fully stopping
// the pattern on real multi-file audits. Moved to the tool's own
// description because: (1) it sits right next to the schema the
// model is already attending to when deciding how to call this
// specific tool, instead of competing with everything else in the
// system prompt; (2) it's part of the tool DEFINITION, sent fresh
// with the same static text on every request, same caching
// properties as the system-prompt version had — never put a date,
// counter, or anything else that varies per-request in here, or it
// breaks Gemini's implicit prefix cache for no benefit.
//
// Three things wrong with the system-prompt version, fixed here:
// (1) a dense file (long Go struct/comment lines) can hit the Read
// tool's own OUTPUT TOKEN cap before 2000 LINES, forcing a real
// "file too large" retry — the old text had no guidance for that
// case, so the model fell back to its own habit of small ~100-line
// pages. Now explicit: retry with a LARGE chunk (500-800 lines),
// never a small one. (2) the old text's "the whole file ends up in
// context either way" framing only holds when the whole file is
// actually needed — now split out as its own bullet, since reading
// one specific function should go through Grep first, not a
// full-file read. (3) "follow this exactly" plus a paragraph of
// justification was wordier than this model needs — shorter, with
// concrete numbers, reads as more authoritative.
// v4 adds three short pieces, all from an audit of this exact text: a
// safety-reassurance line (using only the one fact independently
// verified — the tool's own description already says "by default
// reads up to 2000 lines" — NOT the audit's unverified decompiled
// byte/token-cap numbers from claude.exe, which stay unconfirmed and
// are deliberately left out of this text); a line closing off the
// shell-trick workaround path at the source instead of only
// reactively detecting it (readLoopEquivalentToolName, bashdetect.go);
// and an explicit "check first, don't guess" procedure — the model's
// own stated reason for defaulting to small pages is that it doesn't
// know a file's size before reading it, so give it the concrete
// first step (list the directory / check size) instead of just
// telling it not to guess.
// ReadToolGuidance is readToolGuidance exported for internal/translate's
// tools declaration (the only caller outside this package).
const ReadToolGuidance = "Gateway notice (Read tool): by default call Read with ONLY file_path — no offset, no limit. One call returns the whole file, up to 2000 lines. Never ask for a smaller window to 'save context': paging costs more calls and more context, not less, because every extra call re-sends the whole conversation.\n\nUse offset/limit ONLY when you really need a part of the file: it is longer than 2000 lines, is bigger than about 256 KB, or Read answered that the output is too large (the caps are roughly 25k tokens and 256 KB; Read errors instead of truncating, and a file you can already see is large — wc -l, ls -la, an earlier error — is not worth a no-limit attempt). First ask whether you need the whole file at all. Usually you do not: for a large document get its OUTLINE with one Grep (for Markdown: pattern ^#{1,3} with -n, which lists the headings with line numbers; for code: the function/class definitions), or Grep for the symbol you are after, then Read only those sections with offset/limit. Do not read a large file from the top just to find what is in it — a log or changelog keeps its newest entries at the END. Only when you truly need everything, read it in big windows — the gateway raises any limit below 500 to 500. If you genuinely need the WHOLE of a file that Read refused as too large, call Read again with allow_large: true (when the tool's parameters list it): it returns everything that still fits in the context instead of an error — use it only for that, never as a default. Before reading SEVERAL unfamiliar files (after a Glob or a directory listing), measure them all in ONE call instead of probing one by one: Grep with pattern ^ and output_mode count over the same paths/glob (or wc -l), then read files under ~500 lines whole and the longer ones in windows. To see the END of a long file (newest entries, last log lines), use ONE Read with offset near the end — take the line count from wc -l and set offset to count minus 300 or so — not a chain of small calls. Never walk a file page by page, and do not repeat sed/awk/head/tail/cat/python one-liners over the same file to page through it: it is the same pattern under a different tool."

// NudgeEditFailureLoop fires when Edit has failed (old_string mismatch)
// several times in a row for the same agent — the read-loop guard above
// has no visibility into this at all, since whether an Edit succeeded
// only becomes known on the client's NEXT request (its tool_result's
// is_error), not in this gateway's response to the call that failed.
const NudgeEditFailureLoop = "Notice from the gateway: your Edit tool calls have failed repeatedly because old_string did not match the file. Do not keep retrying minor variations of the same old_string. Read the file to see its exact current contents, then either fix old_string to match exactly or use Write to replace the whole section/file."

// NudgeReadLoopEscalated fires when the soft nudge above already fired
// once for this agent and it slid right back into another full
// read-loop window without ever writing/editing anything in between —
// confirmed real-world pattern: a model politely acknowledges the soft
// nudge ("Understood, I'll proceed") and then immediately reads again.
//
// A live incident caught the old wording — "In your very next turn,
// either call Write/Edit to make the change, or give your final answer
// with what you already have." — reading as an ultimatum to a model that
// was actually status-polling for async subagents to finish (ls/ps
// piped through head, counted as reads by the bug statusListingPipePattern
// above now fixes). Faced with what looked like an imminent hard cutoff,
// the model panicked into a premature Write with incomplete data rather
// than correctly yielding its turn and waiting for the harness's own
// task-completion notification. The wording now names both real
// situations this nudge can fire in — genuinely stuck reading, vs.
// legitimately waiting on background work — instead of assuming the
// former and threatening a deadline that only fits it.
const NudgeReadLoopEscalated = "Notice from the gateway (repeated): you were already warned about an unproductive read-loop and went straight back into another one without writing, editing, or answering in between. If you are gathering context, stop reading and either synthesize your findings into an answer or proceed to implementation with what you already have. If you are waiting on background work (subagents, async tasks) to finish, stop polling for it in this turn — yield the turn now; you will be resumed automatically when it completes."

// RecordPendingNudge stores a pending loop notice on a request/session level,
// to be injected into system_instruction parts rather than polluting tool_result bodies.
func RecordPendingNudge(key, text string) {
	globalLoopGuard.RecordPendingNudge(key, text)
}

// TakePendingNudges returns and clears all pending loop notices for key.
func TakePendingNudges(key string) []string {
	return globalLoopGuard.TakePendingNudges(key)
}
