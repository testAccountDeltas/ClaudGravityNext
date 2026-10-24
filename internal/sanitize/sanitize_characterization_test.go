// Characterization tests for internal/sanitize against v1's
// SanitizeMessagesAndGraph (superrelay/sanitizer.go) — one test per
// audit-caught edge case documented in that function's own comments,
// verifying this port preserves the fixed behavior, not the original bug.
package sanitize

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"claudgravity/internal/editrepair"
	"claudgravity/internal/protocol/claude"
)

func mustReq(t *testing.T, raw string) claude.Request {
	t.Helper()
	var req claude.Request
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	return req
}

func textOf(t *testing.T, b claude.Block) string {
	t.Helper()
	tb, ok := b.AsText()
	if !ok {
		t.Fatalf("block is not text: %+v", b)
	}
	return tb.Text
}

func TestSanitize_LeadingSystemMessage_ExtractedIntoSystemField(t *testing.T) {
	req := mustReq(t, `{
		"model": "claude-opus-4-5", "max_tokens": 10,
		"messages": [
			{"role": "system", "content": "be nice"},
			{"role": "user", "content": "hi"}
		]
	}`)
	res := SanitizeMessagesAndGraph(req)
	if got := res.Request.SystemText(); got != "be nice" {
		t.Fatalf("got system=%q", got)
	}
	if len(res.Request.Messages) != 1 || res.Request.Messages[0].Role != claude.RoleUser {
		t.Fatalf("got messages=%+v", res.Request.Messages)
	}
}

func TestSanitize_ConsecutiveLeadingSystemMessages_BothExtracted(t *testing.T) {
	// A Round-15 audit caught that only messages[0] was ever checked —
	// a second consecutive system-role message became the new head after
	// the first was sliced off, but was never re-checked.
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "system", "content": "first"},
			{"role": "system", "content": "second"},
			{"role": "user", "content": "hi"}
		]
	}`)
	res := SanitizeMessagesAndGraph(req)
	if got := res.Request.SystemText(); got != "first\n\nsecond" {
		t.Fatalf("got system=%q", got)
	}
	if len(res.Request.Messages) != 1 {
		t.Fatalf("expected the stray system message gone, got %+v", res.Request.Messages)
	}
}

func TestSanitize_OnlySystemMessage_LeavesPlaceholderUserTurn(t *testing.T) {
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [{"role": "system", "content": "be nice"}]
	}`)
	res := SanitizeMessagesAndGraph(req)
	if len(res.Request.Messages) != 1 || res.Request.Messages[0].Role != claude.RoleUser {
		t.Fatalf("got messages=%+v", res.Request.Messages)
	}
	if got := textOf(t, res.Request.Messages[0].Content[0]); got != "Continue." {
		t.Fatalf("got placeholder text=%q", got)
	}
}

func TestSanitize_BareStringContent_HollowDotBecomesEllipsis(t *testing.T) {
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [{"role": "assistant", "content": "."}]
	}`)
	res := SanitizeMessagesAndGraph(req)
	// A sole assistant message normalizes to "..." in Step 1, but since
	// the first message must be role "user" (Step 2), a synthetic
	// "Continue." turn is prepended ahead of it — same as v1. The "..."
	// message is pushed to index 1, not 0.
	if len(res.Request.Messages) < 2 {
		t.Fatalf("got messages=%+v", res.Request.Messages)
	}
	if got := textOf(t, res.Request.Messages[1].Content[0]); got != "..." {
		t.Fatalf("bare-string hollow dot must become \"...\", got %q", got)
	}
}

func TestSanitize_ArrayTextBlock_HollowDotDroppedOnlyForAssistant(t *testing.T) {
	// v1 only drops a hollow "." text BLOCK (inside an array) for the
	// assistant role; a user's literal "." block survives untouched.
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [{"role": "user", "content": [{"type": "text", "text": "."}]}]
	}`)
	res := SanitizeMessagesAndGraph(req)
	if got := textOf(t, res.Request.Messages[0].Content[0]); got != "." {
		t.Fatalf("expected a user's literal \".\" block preserved, got %q", got)
	}
}

func TestSanitize_StripsPollutedToolCallNarration(t *testing.T) {
	// GAP-T13: only an assistant turn can emit this pseudo-tool-call
	// narration shape — stripping is scoped to that role. A trailing
	// user turn keeps this out of the unrelated trailing-assistant-prefill
	// extraction path tested elsewhere in this file.
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": "before [Called tool exec_command with input {}] after"},
			{"role": "user", "content": "continue"}
		]
	}`)
	res := SanitizeMessagesAndGraph(req)
	got := textOf(t, res.Request.Messages[1].Content[0])
	if got != "before  after" && got != "before after" {
		t.Fatalf("expected polluted narration stripped, got %q", got)
	}
}

func TestSanitize_PollutedToolCallNarration_UserTextPreserved(t *testing.T) {
	// GAP-T13: a user who pastes a log/text containing a literal
	// "[Called tool ...]" substring must not have it silently stripped —
	// only an assistant's own narration is pseudo-tool-call pollution.
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [{"role": "user", "content": "before [Called tool exec_command with input {}] after"}]
	}`)
	res := SanitizeMessagesAndGraph(req)
	got := textOf(t, res.Request.Messages[0].Content[0])
	if got != "before [Called tool exec_command with input {}] after" {
		t.Fatalf("expected user's text preserved untouched, got %q", got)
	}
}

func TestSanitize_OrphanedToolResult_BecomesText(t *testing.T) {
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "nope", "content": "orphan data"}]}
		]
	}`)
	res := SanitizeMessagesAndGraph(req)
	got := textOf(t, res.Request.Messages[0].Content[0])
	if got != "[Tool output nope]: orphan data" {
		t.Fatalf("got %q", got)
	}
}

func TestSanitize_RoleInvertedToolUse_DegradedToText(t *testing.T) {
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {}}]}
		]
	}`)
	res := SanitizeMessagesAndGraph(req)
	got := textOf(t, res.Request.Messages[0].Content[0])
	want := `[tool_use "Read" in a non-assistant message — ignored]`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSanitize_AbandonedToolUse_SynthesizesRecoveryResult(t *testing.T) {
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {}}]}
		]
	}`)
	res := SanitizeMessagesAndGraph(req)
	last := res.Request.Messages[len(res.Request.Messages)-1]
	if last.Role != claude.RoleUser {
		t.Fatalf("expected a synthesized user turn, got role=%s", last.Role)
	}
	tr, ok := last.Content[0].AsToolResult()
	if !ok || tr.ToolUseID != "t1" {
		t.Fatalf("got content=%+v", last.Content)
	}
	text, _ := tr.ContentText()
	if text != "(recovered by proxy)" {
		t.Fatalf("got %q", text)
	}
	if !tr.IsError {
		t.Fatalf("expected synthesized tool_result to have IsError=true")
	}
}

func TestSanitize_ToolUseOrder_PreservedWhenOneResultAbandoned(t *testing.T) {
	// Two tool_use calls in one assistant turn, only the SECOND answered —
	// the synthesized recovery for the first must still land in original
	// call order ahead of the real answer for the second.
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": [
				{"type": "tool_use", "id": "t1", "name": "Read", "input": {}},
				{"type": "tool_use", "id": "t2", "name": "Read", "input": {}}
			]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t2", "content": "real t2"}]}
		]
	}`)
	res := SanitizeMessagesAndGraph(req)
	var resultTurn claude.Message
	for _, m := range res.Request.Messages {
		if m.Role == claude.RoleUser {
			for _, b := range m.Content {
				if tr, ok := b.AsToolResult(); ok && tr.ToolUseID == "t1" {
					resultTurn = m
				}
			}
		}
	}
	if len(resultTurn.Content) < 2 {
		t.Fatalf("got resultTurn=%+v", resultTurn)
	}
	tr0, _ := resultTurn.Content[0].AsToolResult()
	tr1, _ := resultTurn.Content[1].AsToolResult()
	if tr0.ToolUseID != "t1" || tr1.ToolUseID != "t2" {
		t.Fatalf("expected order t1,t2 — got %s,%s", tr0.ToolUseID, tr1.ToolUseID)
	}
}

// GAP-SN2: SanitizeMessagesAndGraph itself must NOT collapse repeated
// Reads anymore — DedupRepeatedReads is a separate, deliberately later
// step the caller (internal/translate) runs once, after every truncation
// pass. If this regresses (dedup moves back inside
// SanitizeMessagesAndGraph, which runs BEFORE context-window truncation
// in translate.go), a "see above" marker can end up outliving the real
// content it points at, once that content's message gets truncated away.
func TestSanitize_SanitizeMessagesAndGraph_AloneDoesNotDedup(t *testing.T) {
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {"file_path": "a.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "file contents here"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t2", "name": "Read", "input": {"file_path": "a.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t2", "content": "file contents here"}]}
		]
	}`)
	res := SanitizeMessagesAndGraph(req)
	for _, m := range res.Request.Messages {
		for _, b := range m.Content {
			if tr, ok := b.AsToolResult(); ok && tr.ToolUseID == "t2" {
				text, _ := tr.ContentText()
				if text != "file contents here" {
					t.Fatalf("SanitizeMessagesAndGraph alone must leave reads uncollapsed, got %q", text)
				}
			}
		}
	}
}

func TestSanitize_DedupRepeatedRead_CollapsesSecondOccurrence(t *testing.T) {
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {"file_path": "a.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "file contents here"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t2", "name": "Read", "input": {"file_path": "a.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t2", "content": "file contents here"}]}
		]
	}`)
	// GAP-SN2: dedup no longer runs inside SanitizeMessagesAndGraph — the
	// real pipeline (internal/translate) calls DedupRepeatedReads
	// separately, as its own last step.
	sanitized, _ := DedupRepeatedReads(SanitizeMessagesAndGraph(req).Request, "")
	var secondReadResult string
	count := 0
	for _, m := range sanitized.Messages {
		for _, b := range m.Content {
			if tr, ok := b.AsToolResult(); ok && tr.ToolUseID == "t2" {
				text, _ := tr.ContentText()
				secondReadResult = text
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one t2 tool_result, got %d", count)
	}
	if secondReadResult == "file contents here" {
		t.Fatalf("expected the second identical Read to be collapsed into a marker, got raw content back")
	}
}

// An audit caught that a relative path and the absolute path to that
// SAME file (resolved against the session's recorded CWD) produced
// different dedup keys, so a duplicate Read under the other spelling was
// never collapsed. The absolute path here is derived from t.TempDir()
// rather than a hardcoded machine-specific literal (e.g. "C:\..."), so
// the test bakes in no real local path and stays portable across OSes.
func TestSanitize_DedupRepeatedRead_RelativeAndAbsolutePathCollapse(t *testing.T) {
	baseDir := t.TempDir()
	absPath := filepath.Join(baseDir, "main.go")
	sessionID := "sess-pathkey"
	editrepair.RecordSessionCWD(sessionID, baseDir)

	absPathJSON, err := json.Marshal(absPath)
	if err != nil {
		t.Fatalf("marshal abs path: %v", err)
	}
	req := mustReq(t, fmt.Sprintf(`{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {"file_path": "main.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "file contents here"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t2", "name": "Read", "input": {"file_path": %s}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t2", "content": "file contents here"}]}
		]
	}`, string(absPathJSON)))
	sanitized, _ := DedupRepeatedReads(SanitizeMessagesAndGraph(req).Request, sessionID)
	for _, m := range sanitized.Messages {
		for _, b := range m.Content {
			if tr, ok := b.AsToolResult(); ok && tr.ToolUseID == "t2" {
				text, _ := tr.ContentText()
				if text == "file contents here" {
					t.Fatalf("expected the absolute-path re-read of the same file to be collapsed, got raw content back")
				}
			}
		}
	}
}

// GAP-T8: a Read result identical to one seen more than readDecayTurns
// messages ago must be handed back in FULL, not collapsed into a stub —
// a long-lived agent session's attention to exact line numbers has
// typically moved on by then, and a stub starves the very next Edit of
// the fresh context it needs to match old_string correctly.
func TestSanitize_DedupRepeatedRead_StaleAcrossDecayWindow_NotCollapsed(t *testing.T) {
	msgs := []string{`{"role": "user", "content": "go"}`}
	msgs = append(msgs, `{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {"file_path": "a.go"}}]}`)
	msgs = append(msgs, `{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "file contents here"}]}`)
	// readDecayTurns==10: pad with enough filler turns that t2 lands more
	// than 10 messages after t1's tool_result.
	for i := 0; i < 12; i++ {
		msgs = append(msgs, fmt.Sprintf(`{"role": "assistant", "content": "filler %d"}`, i))
		msgs = append(msgs, fmt.Sprintf(`{"role": "user", "content": "ack %d"}`, i))
	}
	msgs = append(msgs, `{"role": "assistant", "content": [{"type": "tool_use", "id": "t2", "name": "Read", "input": {"file_path": "a.go"}}]}`)
	msgs = append(msgs, `{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t2", "content": "file contents here"}]}`)

	req := mustReq(t, fmt.Sprintf(`{"model":"m","max_tokens":10,"messages":[%s]}`, strings.Join(msgs, ",")))
	sanitized, _ := DedupRepeatedReads(SanitizeMessagesAndGraph(req).Request, "")
	var secondReadResult string
	for _, m := range sanitized.Messages {
		for _, b := range m.Content {
			if tr, ok := b.AsToolResult(); ok && tr.ToolUseID == "t2" {
				secondReadResult, _ = tr.ContentText()
			}
		}
	}
	if secondReadResult != "file contents here" {
		t.Fatalf("expected the stale-but-identical second Read to be returned in full, got %q", secondReadResult)
	}
}

func TestSanitize_DedupRepeatedRead_IsErrorNeverCollapsed(t *testing.T) {
	// A Round-15 audit caught the original had no is_error check — a
	// repeated READ ERROR (same message both times) got collapsed as if
	// it were a successful duplicate, hiding the real error text.
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {"file_path": "bad.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "is_error": true, "content": "permission denied"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t2", "name": "Read", "input": {"file_path": "bad.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t2", "is_error": true, "content": "permission denied"}]}
		]
	}`)
	sanitized, _ := DedupRepeatedReads(SanitizeMessagesAndGraph(req).Request, "")
	for _, m := range sanitized.Messages {
		for _, b := range m.Content {
			if tr, ok := b.AsToolResult(); ok && tr.ToolUseID == "t2" {
				text, _ := tr.ContentText()
				if text != "permission denied" {
					t.Fatalf("an is_error tool_result must never be collapsed, got %q", text)
				}
			}
		}
	}
}

func TestSanitize_TrailingAssistantTurn_AppendsPrefillNudge(t *testing.T) {
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "write json"},
			{"role": "assistant", "content": "{"}
		]
	}`)
	res := SanitizeMessagesAndGraph(req)
	if res.PrefillText != "{" {
		t.Fatalf("got PrefillText=%q", res.PrefillText)
	}
	last := res.Request.Messages[len(res.Request.Messages)-1]
	if last.Role != claude.RoleUser {
		t.Fatalf("conversation must end on a user turn, got role=%s", last.Role)
	}
}

// GAP-T7: a tool_result that's itself a valid JSON array (grep/search
// matches, a paginated listing) must stay valid JSON after truncation —
// a raw byte cut through the middle used to land inside some element's
// string/number literal and hand the model unparseable JSON.
func TestTruncateMiddle_JSONArray_StaysValidJSON(t *testing.T) {
	elems := make([]string, 0, 5000)
	for i := 0; i < 5000; i++ {
		elems = append(elems, fmt.Sprintf(`{"line":%d,"text":"match number %d in the file"}`, i, i))
	}
	raw := "[" + strings.Join(elems, ",") + "]"
	if len(raw) <= ToolOutputCharLimit {
		t.Fatalf("test fixture too small to trigger truncation: %d bytes", len(raw))
	}
	out, truncated := TruncateMiddle(raw)
	if !truncated {
		t.Fatal("expected truncation to trigger")
	}
	var decoded []json.RawMessage
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("expected valid JSON array after truncation, got error %v for %s", err, out[:200])
	}
	if len(out) > ToolOutputCharLimit {
		t.Fatalf("expected result within the char limit, got %d bytes", len(out))
	}
}

func TestSanitize_ToolResultTruncation_SetsTruncatedAny(t *testing.T) {
	big := make([]byte, ToolOutputCharLimit+1000)
	for i := range big {
		big[i] = 'x'
	}
	rawContent, _ := json.Marshal(string(big))
	reqJSON := `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": ` + string(rawContent) + `}]}
		]
	}`
	req := mustReq(t, reqJSON)
	res := SanitizeMessagesAndGraph(req)
	if !res.TruncatedAny {
		t.Fatal("expected TruncatedAny=true for an over-limit tool_result")
	}
}

func TestSanitize_CacheControlOnToolResult_SurvivesTruncation(t *testing.T) {
	big := make([]byte, ToolOutputCharLimit+1000)
	for i := range big {
		big[i] = 'x'
	}
	rawContent, _ := json.Marshal(string(big))
	reqJSON := `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "cache_control": {"type": "ephemeral"}, "content": ` + string(rawContent) + `}]}
		]
	}`
	req := mustReq(t, reqJSON)
	res := SanitizeMessagesAndGraph(req)
	for _, m := range res.Request.Messages {
		for _, b := range m.Content {
			if tr, ok := b.AsToolResult(); ok && tr.ToolUseID == "t1" {
				if len(tr.CacheControl) == 0 {
					t.Fatal("cache_control must survive a truncation rebuild")
				}
			}
		}
	}
}

// GAP-P1-05: a raw JSON OBJECT tool_result content (some MCP/custom
// tools emit this shape directly, not a bare string or a block array)
// used to skip the 64KB truncation entirely — both the bare-string and
// block-array unmarshal attempts fail for an object, falling through
// untouched.
func TestSanitize_ToolResultTruncation_AppliesToRawJSONObject(t *testing.T) {
	bigVal := make([]byte, ToolOutputCharLimit+1000)
	for i := range bigVal {
		bigVal[i] = 'x'
	}
	objContent := fmt.Sprintf(`{"result":%q}`, string(bigVal))
	reqJSON := `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": ` + objContent + `}]}
		]
	}`
	req := mustReq(t, reqJSON)
	res := SanitizeMessagesAndGraph(req)
	if !res.TruncatedAny {
		t.Fatal("expected TruncatedAny=true for an over-limit raw-object tool_result")
	}
}

// GAP-D4-04: a tool_result physically appearing BEFORE its own tool_use
// in the message array (corrupted/replayed history) must still be
// recognized as orphaned (converted to text) — the old two-full-pass
// design built its "seen" set from EVERY message regardless of order,
// so this case was wrongly treated as matched.
func TestSanitize_ToolResultBeforeItsOwnToolUse_TreatedAsOrphaned(t *testing.T) {
	reqJSON := `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "early"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "late"}]}
		]
	}`
	req := mustReq(t, reqJSON)
	res := SanitizeMessagesAndGraph(req)
	// Exactly one real tool_result for t1 must survive (the one AFTER
	// the real tool_use) — the premature one must have been degraded to
	// plain text, not left as a second real tool_result for the same id.
	realToolResultCount := 0
	for _, m := range res.Request.Messages {
		for _, b := range m.Content {
			if tr, ok := b.AsToolResult(); ok && tr.ToolUseID == "t1" {
				realToolResultCount++
			}
		}
	}
	if realToolResultCount != 1 {
		t.Fatalf("expected exactly 1 real tool_result for t1 to survive (the premature one degraded to text), got %d in %+v", realToolResultCount, res.Request.Messages)
	}
}

// GAP-D4-05: a mid-conversation role:"system" message (not at the front
// of the array — those are handled separately) must be normalized to
// role:"user" instead of left as "system", which breaks the alternating
// pairing logic downstream.
func TestSanitize_MidConversationSystemMessage_NormalizedToUser(t *testing.T) {
	reqJSON := `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "go"},
			{"role": "assistant", "content": "ok"},
			{"role": "system", "content": "reminder: be concise"},
			{"role": "user", "content": "continue"}
		]
	}`
	req := mustReq(t, reqJSON)
	res := SanitizeMessagesAndGraph(req)
	for _, m := range res.Request.Messages {
		if m.Role == claude.Role("system") {
			t.Fatalf("expected no mid-conversation role:system to survive, got %+v", res.Request.Messages)
		}
	}
	if !res.Modified {
		t.Fatal("expected Modified=true for a normalized mid-conversation system message")
	}
}

func TestSanitize_StripANSIEscapeCodes_FromToolResult(t *testing.T) {
	colored := "\x1b[32mPASS\x1b[0m: all 42 tests passed\x1b[1m in 0.5s\x1b[0m"
	coloredJSON, _ := json.Marshal(colored)
	reqJSON := fmt.Sprintf(`{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "run tests"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Bash", "input": {"command": "go test"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": %s}]}
		]
	}`, string(coloredJSON))
	req := mustReq(t, reqJSON)
	res := SanitizeMessagesAndGraph(req)
	var text string
	for _, m := range res.Request.Messages {
		for _, b := range m.Content {
			if tr, ok := b.AsToolResult(); ok && tr.ToolUseID == "t1" {
				text, _ = tr.ContentText()
			}
		}
	}
	if strings.Contains(text, "\x1b") {
		t.Fatalf("expected ANSI escape codes to be stripped, got %q", text)
	}
	if text != "PASS: all 42 tests passed in 0.5s" {
		t.Fatalf("unexpected content after ANSI stripping: %q", text)
	}
}

// -----------------------------------------------------------------------------
// DedupRepeatedReads Audit Tests
// -----------------------------------------------------------------------------

func TestSanitize_DedupRepeatedRead_ModifiedContentPreserved(t *testing.T) {
	req := mustReq(t, `{
		"model": "m", "max_tokens": 10,
		"messages": [
			{"role": "user", "content": "read v1"},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t1", "name": "Read", "input": {"file_path": "file.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t1", "content": "version 1"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t2", "name": "Read", "input": {"file_path": "file.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t2", "content": "version 2"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "t3", "name": "Read", "input": {"file_path": "file.go"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "t3", "content": "version 2"}]}
		]
	}`)

	sanitized, changed := DedupRepeatedReads(req, "")
	if !changed {
		t.Fatal("expected DedupRepeatedReads to report changed=true (for t3)")
	}

	results := make(map[string]string)
	for _, m := range sanitized.Messages {
		for _, b := range m.Content {
			if tr, ok := b.AsToolResult(); ok {
				text, _ := tr.ContentText()
				results[tr.ToolUseID] = text
			}
		}
	}

	if results["t1"] != "version 1" {
		t.Errorf("t1 content: got %q, want %q", results["t1"], "version 1")
	}
	if results["t2"] != "version 2" {
		t.Errorf("t2 content: got %q, want %q (modified file content must be preserved!)", results["t2"], "version 2")
	}
	if !strings.HasPrefix(results["t3"], "[Identical to the Read result already shown earlier") {
		t.Errorf("t3 content: got %q, want collapsed marker", results["t3"])
	}
}

func TestSanitize_DedupRepeatedRead_ExactDecayBoundary(t *testing.T) {
	buildReq := func(padMsgs int) claude.Request {
		var msgs []claude.Message
		msgs = append(msgs, claude.Message{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: "start"})}})
		msgs = append(msgs, claude.Message{Role: claude.RoleAssistant, Content: []claude.Block{claude.NewBlock(claude.ToolUseBlock{Type: "tool_use", ID: "t1", Name: "Read", Input: json.RawMessage(`{"file_path":"a.go"}`)})}})
		msgs = append(msgs, claude.Message{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.ToolResultBlock{Type: "tool_result", ToolUseID: "t1", Content: json.RawMessage(`"contents"`)})}})
		for i := 0; i < padMsgs; i++ {
			role := claude.RoleAssistant
			if i%2 == 1 {
				role = claude.RoleUser
			}
			msgs = append(msgs, claude.Message{Role: role, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: fmt.Sprintf("filler %d", i)})}})
		}
		msgs = append(msgs, claude.Message{Role: claude.RoleAssistant, Content: []claude.Block{claude.NewBlock(claude.ToolUseBlock{Type: "tool_use", ID: "t2", Name: "Read", Input: json.RawMessage(`{"file_path":"a.go"}`)})}})
		msgs = append(msgs, claude.Message{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.ToolResultBlock{Type: "tool_result", ToolUseID: "t2", Content: json.RawMessage(`"contents"`)})}})
		return claude.Request{Model: "m", Messages: msgs}
	}

	req10 := buildReq(8) // mi distance = 10
	san10, changed10 := DedupRepeatedReads(req10, "")
	if !changed10 {
		t.Error("expected distance 10 to collapse")
	}
	t2Text10, _ := san10.Messages[len(san10.Messages)-1].Content[0].AsToolResult()
	t2Content10, _ := t2Text10.ContentText()
	if !strings.HasPrefix(t2Content10, "[Identical to the Read result") {
		t.Errorf("distance 10 expected collapsed marker, got %q", t2Content10)
	}

	req11 := buildReq(9) // mi distance = 11
	san11, changed11 := DedupRepeatedReads(req11, "")
	if changed11 {
		t.Error("expected distance 11 to NOT collapse (stale decay window)")
	}
	t2Text11, _ := san11.Messages[len(san11.Messages)-1].Content[0].AsToolResult()
	t2Content11, _ := t2Text11.ContentText()
	if t2Content11 != "contents" {
		t.Errorf("distance 11 expected raw contents preserved, got %q", t2Content11)
	}
}

func TestSanitize_DedupRepeatedRead_RelativePathNotMutatedInInput(t *testing.T) {
	sessionID := "sess_rel_check"
	baseDir := t.TempDir()
	editrepair.RecordSessionCWD(sessionID, baseDir)

	origInput := `{"file_path":"relative/path/file.go"}`
	req := claude.Request{
		Model: "m",
		Messages: []claude.Message{
			{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.TextBlock{Type: "text", Text: "read"})}},
			{Role: claude.RoleAssistant, Content: []claude.Block{claude.NewBlock(claude.ToolUseBlock{Type: "tool_use", ID: "r1", Name: "Read", Input: json.RawMessage(origInput)})}},
			{Role: claude.RoleUser, Content: []claude.Block{claude.NewBlock(claude.ToolResultBlock{Type: "tool_result", ToolUseID: "r1", Content: json.RawMessage(`"data"`)})}},
		},
	}

	sanitized, _ := DedupRepeatedReads(req, sessionID)

	tu, _ := sanitized.Messages[1].Content[0].AsToolUse()
	if string(tu.Input) != origInput {
		t.Errorf("tu.Input was mutated! got %s, want %s", string(tu.Input), origInput)
	}
}

// Empty role:"system" messages mid-conversation (content "", [] or
// [{text:""}]) must be dropped, not turned into a bare "[System Note]: "
// user turn; an output_config they carry moves to the adjacent turn.
func TestSanitize_EmptyMidConversationSystemMessage_Dropped(t *testing.T) {
	for _, empty := range []string{`""`, `[]`, `[{"type":"text","text":""}]`} {
		reqJSON := `{
			"model": "m", "max_tokens": 10,
			"messages": [
				{"role": "user", "content": "go"},
				{"role": "assistant", "content": "ok"},
				{"role": "system", "content": ` + empty + `, "output_config": {"effort": "low"}},
				{"role": "user", "content": "continue"}
			]
		}`
		res := SanitizeMessagesAndGraph(mustReq(t, reqJSON))
		for _, m := range res.Request.Messages {
			for _, b := range m.Content {
				if tb, ok := b.AsText(); ok && strings.Contains(tb.Text, "[System Note]") {
					t.Fatalf("content %s: empty system message became %q", empty, tb.Text)
				}
			}
		}
		if len(res.Request.Messages) != 3 {
			t.Fatalf("content %s: want 3 messages after dropping, got %d", empty, len(res.Request.Messages))
		}
		last := res.Request.Messages[len(res.Request.Messages)-1]
		if !strings.Contains(string(last.OutputConfig), "low") {
			t.Fatalf("content %s: output_config effort not carried to adjacent turn: %q", empty, last.OutputConfig)
		}
	}
}
