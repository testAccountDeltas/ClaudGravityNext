// Part of the translate package — see translate.go for the package doc
// comment. Direct port of v1's translator.go Step 2 (LoopGuard nudge
// wiring) and lastTurnHadFailedEdit.
package translate

import (
	"encoding/json"
	"strings"

	"claudgravity/internal/logx"
	"claudgravity/internal/loopguard"
	"claudgravity/internal/protocol/claude"
)

// applyLoopGuardNudges runs v1's Step 2 against reqTyped: read-loop and
// tool-call-oscillation nudges, and the consecutive-Edit-failure nudge.
// May return a new Request value (when a nudge was spliced into the
// conversation tail) — the caller must use the returned Request from
// here on, matching the in-place-mutation semantics v1's shared
// reqMap had.
func applyLoopGuardNudges(req claude.Request, agentKey, sessionID, instanceID string) claude.Request {
	// loopKey disambiguates both across sessions and across parallel
	// subagents within one session: agentKey alone (e.g. "sub:general-
	// purpose") is shared by every concurrent instance of that subagent
	// type, in every session the gateway is serving, and even bare "main"
	// was one single key shared by every session's main thread. sessionID
	// scopes it to one conversation; instanceID (stable per subagent run)
	// further separates concurrent subagents of the same type within that
	// same session.
	loopKey := agentKey + "|" + sessionID + "|" + instanceID

	// GAP-LG-01: this used to have its OWN separate hasModifyingTools
	// check, scanning only DECLARED tools — diverging from
	// loopguard/planmode.go's hasModifyingToolsInRequest, which an
	// earlier audit pass fixed to check actual past tool_use calls
	// instead (declared-only made it true for nearly every session,
	// since Claude Code declares Edit/Write even during pure read-only
	// review). That fix never reached this file, since it had its own
	// copy of the logic rather than calling the shared one. Route
	// through the same shared, now-fixed function instead of
	// maintaining a second, silently-stale implementation.
	isPlanMode := false
	for _, t := range req.Tools {
		if strings.EqualFold(t.Name, "exitplanmode") {
			isPlanMode = true
			break
		}
	}
	var hasModifyingTools bool
	if !isPlanMode {
		if reqBytes, err := json.Marshal(req); err == nil {
			hasModifyingTools = loopguard.HasModifyingToolsInRequest(reqBytes)
		}
	}

	lg := loopguard.Global()

	// GAP-07: If the user prompt explicitly requested ignoring loop detection, reset oscillation history.
	for _, m := range req.Messages {
		if m.Role == claude.RoleUser {
			for _, b := range m.Content {
				if t, ok := b.AsText(); ok && loopguard.HasIgnoreLoopTag(t.Text) {
					lg.ResetToolCallHistory(loopKey)
					break
				}
			}
		}
	}
	_, readThreshold := loopguard.ReadLoopLimits(agentKey, hasModifyingTools)
	// Sync Plan Mode state here first — RecordReadTurnWithRealProgress
	// (called elsewhere, after THIS request's Gemini response comes back)
	// is one request too late for deciding whether to nudge THIS outgoing
	// request on the exact turn that exits Plan Mode.
	lg.SyncPlanMode(loopKey, isPlanMode)
	if level := lg.CheckAndResetReadStreak(loopKey, readThreshold); level > 0 {
		logx.LogGateway("[LoopGuard] read-loop nudge level %d for %s", level, loopKey)
		nudgeText := loopguard.NudgeReadLoop
		if level >= 2 {
			nudgeText = loopguard.NudgeReadLoopEscalated
		}
		var ok bool
		req, ok = injectNudgeIntoTail(req, nudgeText)
		if !ok {
			recordLoopNudge(sessionID, loopKey, nudgeText)
		}
	}
	if lastTurnReadTooLarge(req) {
		logx.LogGateway("[LoopGuard] Read reported the output too large; shrinking the widened Read size for %s", loopKey)
		lg.NoteReadTooLarge(loopKey)
	}
	if lg.CheckAndResetOscillation(loopKey) {
		var ok bool
		req, ok = injectNudgeIntoTail(req, loopguard.NudgeOscillation)
		if !ok {
			recordLoopNudge(sessionID, loopKey, loopguard.NudgeOscillation)
		}
	}
	if hadEdit, editFailed := lastTurnHadFailedEdit(req); hadEdit {
		if editFailStreak := lg.RecordEditResult(loopKey, editFailed); editFailStreak >= 3 {
			var ok bool
			req, ok = injectNudgeIntoTail(req, loopguard.NudgeEditFailureLoop)
			if !ok {
				recordLoopNudge(sessionID, loopKey, loopguard.NudgeEditFailureLoop)
			}
		}
	}
	return req
}

// lastTurnReadTooLarge reports whether the newest user message carries a Read
// error saying the output exceeded the token cap, so the gateway can stop
// widening Reads to a size this file cannot return.
func lastTurnReadTooLarge(req claude.Request) bool {
	if len(req.Messages) == 0 {
		return false
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != claude.RoleUser {
		return false
	}
	for _, b := range last.Content {
		if tr, ok := b.AsToolResult(); ok && tr.IsError && strings.Contains(string(tr.Content), "exceeds maximum allowed tokens") {
			return true
		}
	}
	return false
}

func injectNudgeIntoTail(req claude.Request, text string) (claude.Request, bool) {
	if len(req.Messages) == 0 {
		return req, false
	}
	lastIdx := len(req.Messages) - 1
	if req.Messages[lastIdx].Role != claude.RoleUser {
		return req, false
	}
	nudgeRaw, _ := json.Marshal(claude.TextBlock{Type: "text", Text: text})
	req.Messages[lastIdx].Content = append(req.Messages[lastIdx].Content, claude.Block{
		Type: "text",
		Raw:  nudgeRaw,
	})
	return req, true
}

func recordLoopNudge(sessionID, loopKey, text string) {
	logx.LogGateway("[LoopGuard] nudge could not ride on the request tail, queued for the system prompt (%s)", loopKey)
	if loopKey != "" {
		loopguard.RecordPendingNudge(loopKey, text)
	} else if sessionID != "" {
		loopguard.RecordPendingNudge(sessionID, text)
	}
}

// lastTurnHadFailedEdit reports whether the most recent assistant/user
// turn pair in req carries an Edit (or str_replace_editor/update/
// notebookedit) tool_use answered by a tool_result with is_error:true —
// the earliest point this gateway can see that outcome at all, since the
// client only reports it on its NEXT request.
//
// Returning whether an edit happened AT ALL, separately from whether it
// failed, lets the caller leave the streak alone on a non-edit turn
// instead of treating it as a reset: a turn with NO edit at all (e.g. the
// model called Read to re-check context after a failed edit) must not
// reset a streak the same way a turn whose edit actually succeeded would.
func lastTurnHadFailedEdit(req claude.Request) (hadEdit bool, failed bool) {
	msgs := req.Messages
	if len(msgs) < 2 {
		return false, false
	}
	last := msgs[len(msgs)-1]
	if last.Role != claude.RoleUser {
		return false, false
	}
	prev := msgs[len(msgs)-2]
	if prev.Role != claude.RoleAssistant {
		return false, false
	}

	editToolIDs := make(map[string]bool)
	for _, b := range prev.Content {
		tu, ok := b.AsToolUse()
		if !ok {
			continue
		}
		switch strings.ToLower(tu.Name) {
		case "edit", "str_replace_editor", "update", "notebookedit":
			if tu.ID != "" {
				editToolIDs[tu.ID] = true
			}
		}
	}
	if len(editToolIDs) == 0 {
		return false, false
	}

	// Only report failure when EVERY edit in the turn failed — a
	// multi-file batch where only one obscure file's edit failed is
	// forward progress, not a stuck loop, and must not count toward the
	// same 3-strike streak as a genuinely repeated failure on one file.
	successCount, failedCount := 0, 0
	for _, b := range last.Content {
		tr, ok := b.AsToolResult()
		if !ok || !editToolIDs[tr.ToolUseID] {
			continue
		}
		hadEdit = true
		if tr.IsError {
			failedCount++
		} else {
			successCount++
		}
	}
	failed = failedCount > 0 && successCount == 0
	return hadEdit, failed
}
