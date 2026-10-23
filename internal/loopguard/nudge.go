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
const ReadToolGuidance = "Gateway notice (Read tool): Do not read files in small pages. Call Read without offset/limit by default: it returns up to 2000 lines in one call, which covers most files, and the tool itself safely handles anything too large — you do not need to protect against that yourself. Splitting into 100-150 line pages wastes calls and time and saves no context, because the whole file ends up in context anyway.\n\nIf this conversation already showed this file's size — an earlier ls -la, find, or directory listing — use that number, don't default to a cautious small chunk out of habit when you already know the answer. If you don't know the size at all, check first (ls -la) instead of guessing. Under ~150KB: always read it whole, one call, no offset/limit. That covers the large majority of individual source files.\n\nUse offset/limit only when:\n- the file is confirmed at or above that size (then use big chunks, at least 500-800 lines, never 100-150), or\n- a Grep/Bash search already gave you the exact line range you need, and you only need that range.\n\nTo locate one function or symbol in a large file, Grep first, then Read just that range. Do not scan the file page by page. Switching to sed/awk/seq/head/tail/cat/python one-liners to page through a file the same way does not help — it is the same pattern under a different tool and is tracked the same way."

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

// InjectUserNudge injects a synthetic user message into the tail of the conversation
// without modifying system instructions, guaranteeing 100% byte invariance for the KV-cache prefix.
// If the tail turn contains tool_result blocks, notices are never injected into the tool_result
// bodies (which corrupts structured MCP/subagent JSON and causes Gemini to echo loop tags);
// instead, notices are stored as pending loop notices for injection into system_instruction.
func InjectUserNudge(claudeReqJSON map[string]interface{}, text string) {
	msgsRaw, ok := claudeReqJSON["messages"]
	if !ok {
		return
	}
	var msgs []interface{}
	switch v := msgsRaw.(type) {
	case []interface{}:
		msgs = v
	case []map[string]interface{}:
		// An audit caught that this only ever accepted []interface{} —
		// the sanitizer's own normalization has previously left
		// "messages" as []map[string]interface{} at points in its
		// pipeline, and a caller that still sees that shape here hit
		// this type assertion's failure path and silently did nothing,
		// with no error or log to notice it by. Maps are reference
		// types, so wrapping each one loses no ability to mutate it in
		// place below.
		msgs = make([]interface{}, len(v))
		for i, m := range v {
			msgs[i] = m
		}
	default:
		return
	}

	nudgeBlock := map[string]interface{}{"type": "text", "text": text}

	// If the conversation already ends on a "user" turn, append into it
	// instead of adding a second consecutive "user" message — Gemini
	// expects strict user/model alternation, and this guard fires exactly
	// when the model is misbehaving, which is the worst time to risk an
	// upstream 400 on top of it.
	if len(msgs) > 0 {
		if lastMsg, ok := msgs[len(msgs)-1].(map[string]interface{}); ok {
			if role, _ := lastMsg["role"].(string); role == "assistant" {
				// A Round 15 audit caught that falling through to the
				// unconditional append below (a brand-new trailing "user"
				// message) breaks the Anthropic assistant-prefill contract
				// whenever the client's last turn is this same trailing
				// assistant message on purpose — the model is meant to
				// CONTINUE that exact text, which requires the conversation
				// to still end on an assistant turn. Appending a user turn
				// after it is read by Gemini as a brand new exchange
				// instead, silently discarding the prefill. This function's
				// own doc comment already rules out folding the nudge into
				// the system instruction (breaks the KV-cache-prefix byte
				// invariance it exists to guarantee), so the safest choice
				// is to skip injecting the nudge for this one request
				// rather than corrupt the prefill — a request that both
				// trips LoopGuard AND carries a trailing prefill is rare,
				// and losing one nudge cycle is far cheaper than a broken
				// prefill contract.
				return
			}
			if role, _ := lastMsg["role"].(string); role == "user" {
				switch rawContent := lastMsg["content"].(type) {
				case []map[string]interface{}:
					content := make([]interface{}, len(rawContent))
					for i, m := range rawContent {
						content[i] = m
					}
					// Check if this turn carries a tool_result. Loop notices must never
					// be injected into tool_result blocks — doing so corrupts structured
					// tool output (JSON from MCP tools or subagents) and causes Gemini
					// to echo loop tags. Store as pending notice instead.
					hasToolResult := false
					for _, item := range content {
						if block, ok := item.(map[string]interface{}); ok && block["type"] == "tool_result" {
							hasToolResult = true
							break
						}
					}
					if hasToolResult {
						var sessionKey string
						if s, ok := claudeReqJSON["session_id"].(string); ok && s != "" {
							sessionKey = s
						}
						RecordPendingNudge(sessionKey, text)
						return
					}
					lastMsg["content"] = append(content, nudgeBlock)
					claudeReqJSON["messages"] = msgs
					return
				case []interface{}:
					content := rawContent
					hasToolResult := false
					for _, item := range content {
						if block, ok := item.(map[string]interface{}); ok && block["type"] == "tool_result" {
							hasToolResult = true
							break
						}
					}
					if hasToolResult {
						var sessionKey string
						if s, ok := claudeReqJSON["session_id"].(string); ok && s != "" {
							sessionKey = s
						}
						RecordPendingNudge(sessionKey, text)
						return
					}
					lastMsg["content"] = append(content, nudgeBlock)
					claudeReqJSON["messages"] = msgs
					return
				case string:
					lastMsg["content"] = []interface{}{
						map[string]interface{}{"type": "text", "text": rawContent},
						nudgeBlock,
					}
					claudeReqJSON["messages"] = msgs
					return
				}
			}
		}
	}

	nudgeMsg := map[string]interface{}{
		"role":    "user",
		"content": []map[string]interface{}{nudgeBlock},
	}

	claudeReqJSON["messages"] = append(msgs, nudgeMsg)
}
