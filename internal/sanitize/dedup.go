// Part of the sanitize package — see text.go for the package doc comment.
// This file ports v1's Step 6: collapsing repeated identical Read results
// into a short marker, the single most common shape of the "unproductive
// read-loop" internal/loopguard exists to catch.
package sanitize

import (
	"encoding/json"
	"fmt"
	"strings"

	"claudgravity/internal/editrepair"
	"claudgravity/internal/pathkey"
	"claudgravity/internal/protocol/claude"
)

// dedupeRepeatedReads ports v1's dedupedRepeatedReads: a Read tool_result
// byte-identical to an earlier Read of the same (canonicalized) file path
// — with the same offset/limit/pages — elsewhere in this conversation is
// collapsed into a marker; the first occurrence, and any genuinely changed
// re-read, is left untouched.
// readDecayTurns: once more than this many messages (~conversation turns)
// separate a Read from the last time its (path+offset/limit) was seen, a
// fresh re-read is given back in full rather than deduped away — GAP-T8.
// A long-lived agent session's attention to exact line numbers/indentation
// on a file read 40 turns ago has typically moved on; keeping only a stub
// marker there starves the model of the fresh context an immediately
// following Edit on that same file needs to match old_string correctly.
const readDecayTurns = 10

// DedupRepeatedReads applies dedupeRepeatedReads to req.Messages and
// returns a new Request plus whether anything changed.
//
// GAP-SN2: this is deliberately NOT called from SanitizeMessagesAndGraph
// (where it used to live, folded in with every other sanitize step) —
// internal/translate.ConvertClaudeRequestToGemini calls
// SanitizeMessagesAndGraph BEFORE internal/tokens.TruncateConversationToFit
// drops the oldest messages to fit the model's context window. If the
// FIRST occurrence of an identical Read (the one holding the real file
// content) happened to be old enough to get truncated away, while the
// SECOND occurrence (already collapsed into a short "see above" marker by
// dedup's earlier pass) survived because it's more recent, the model was
// left with a marker pointing at content that had already been deleted
// from its context entirely — strictly worse than not deduping at all.
// The caller must run this ONCE, as the LAST step before serializing the
// request, after every truncation pass has already decided which
// messages survive.
func DedupRepeatedReads(req claude.Request, sessionID string) (claude.Request, bool) {
	newMsgs, changed := dedupeRepeatedReads(req.Messages, sessionID)
	if !changed {
		return req, false
	}
	req.Messages = newMsgs
	return req, true
}

func dedupeRepeatedReads(msgs []claude.Message, sessionID string) ([]claude.Message, bool) {
	toolIDToPath := make(map[string]string)
	seenContent := make(map[string]seenRead)
	modified := false
	// An audit caught that a relative path ("main.go") and the absolute
	// path to that SAME file (the session's CWD joined with "main.go")
	// produced different dedup keys (pathkey.Canonical only normalizes
	// slash direction and drive-letter case, never resolves a relative
	// path against any CWD), so a duplicate Read under the other spelling
	// was never collapsed. Resolve against the session's own recorded CWD
	// (same lookup internal/editrepair's RepairEditToolCall uses) before
	// canonicalizing, same as that package's own isAbsPath/joinCWD
	// pattern — exported from there rather than re-implemented here.
	cwd := editrepair.GetSessionCWD(sessionID)

	out := make([]claude.Message, len(msgs))
	for mi, m := range msgs {
		newBlocks := make([]claude.Block, len(m.Content))
		for bi, b := range m.Content {
			switch b.Type {
			case "tool_use":
				newBlocks[bi] = b
				recordReadToolUse(b, toolIDToPath, cwd)
			case "tool_result":
				newBlock, changed := dedupeOneToolResult(b, toolIDToPath, seenContent, mi)
				newBlocks[bi] = newBlock
				if changed {
					modified = true
				}
			default:
				newBlocks[bi] = b
			}
		}
		out[mi] = claude.Message{Role: m.Role, Content: newBlocks, OutputConfig: m.OutputConfig}
	}
	return out, modified
}

func recordReadToolUse(b claude.Block, toolIDToPath map[string]string, cwd string) {
	tu, _ := b.AsToolUse()
	if !strings.EqualFold(tu.Name, "Read") || tu.ID == "" {
		return
	}
	var input map[string]interface{}
	if json.Unmarshal(tu.Input, &input) != nil || input == nil {
		return
	}
	path := firstNonEmptyString(input, "file_path", "path", "file", "target_file")
	if path == "" {
		return
	}
	if cwd != "" && !editrepair.IsAbsPath(path) {
		path = editrepair.JoinCWD(cwd, path)
	}
	key := pathkey.Canonical(path)
	if offset, ok := input["offset"]; ok && offset != nil {
		key = fmt.Sprintf("%s:off=%v", key, offset)
	}
	if limit, ok := input["limit"]; ok && limit != nil {
		key = fmt.Sprintf("%s:lim=%v", key, limit)
	}
	if pages, ok := input["pages"]; ok && pages != nil {
		key = fmt.Sprintf("%s:pages=%v", key, pages)
	}
	toolIDToPath[tu.ID] = key
}

// seenRead records the plain text of a prior Read result and which
// message index (turn) it appeared at, so dedupeOneToolResult can apply
// the readDecayTurns cutoff.
type seenRead struct {
	text string
	mi   int
}

func dedupeOneToolResult(b claude.Block, toolIDToPath map[string]string, seenContent map[string]seenRead, mi int) (claude.Block, bool) {
	tr, ok := b.AsToolResult()
	if !ok || tr.IsError {
		return b, false
	}
	path, ok := toolIDToPath[tr.ToolUseID]
	if !ok {
		return b, false
	}
	text := toolResultPlainText(tr.Content)
	if text == "" {
		return b, false
	}
	// internal/loopguard's InjectUserNudge folds its nudge text directly
	// into a tool_result's content when that's the turn's last turn —
	// strip a trailing nudge suffix before comparing/caching, or a later
	// genuine re-read's clean (un-suffixed) content never matches the
	// cached nudge-suffixed value.
	if idx := strings.Index(text, "\n\n[Notice from the gateway"); idx != -1 {
		// An audit caught that slicing here can leave a trailing "\r"
		// behind when the content before the marker came from a CRLF
		// file (the slice point is right after the "\n" of "\r\n\n[Notice",
		// swallowing only the "\n" and leaving the lone "\r" attached) —
		// a later genuine re-read's clean text (no "\r") would then fail
		// the exact prev.text == text comparison below and never dedup.
		text = strings.TrimRight(text[:idx], "\r")
	}
	if text == "" {
		return b, false
	}
	// On a second pass over the same messages, a block this function
	// already marked holds the marker text itself, not the real file
	// content — never cache that back under seenContent[path].
	if strings.HasPrefix(text, "[Identical to the Read result already shown earlier") {
		return b, false
	}
	if prev, seen := seenContent[path]; seen && prev.text == text {
		if mi-prev.mi > readDecayTurns {
			// Too stale to dedup away — refresh the cached turn index so
			// repeated reads WITHIN the next decay window still collapse,
			// but hand this one back in full.
			seenContent[path] = seenRead{text: text, mi: mi}
			return b, false
		}
		marker := fmt.Sprintf("[Identical to the Read result already shown earlier in this conversation for %s — unchanged, see above. Do not re-read; proceed with what you already have.]", path)
		tr.Content = mustMarshal(marker)
		return claude.NewBlock(tr), true
	}
	seenContent[path] = seenRead{text: text, mi: mi}
	return b, false
}

func firstNonEmptyString(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
