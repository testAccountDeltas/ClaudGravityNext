// Package rules holds the 12 speccheck invariants cmd/speccheck runs
// against real gateway traffic captured by internal/speccapture into
// request_captures/*.ndjson — see
// ../superrelay/ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md
// Part 7.6 for the full numbered list and the reasoning behind each one.
// Rules 1-7 are a faithful transcription of superrelay (v1)'s
// cmd/speccheck/rules.go — that file's own doc comment says they're
// "already correctly designed, language-independent," so the logic is
// carried over as-is, only re-homed into one file per rule per this
// package's registry.go. Rules 8-12 are new: they were never implemented
// anywhere (confirmed by reading v1's rules.go in full — it has exactly
// the 7 rules below), built from this document's Part 7.6 description,
// not ported from existing code.
//
// This file holds the shared infrastructure every rule file needs:
// CaptureRecord (the generic, gateway-decoupled decode of one NDJSON
// line — same reasoning as v1's: this package never imports
// claudgravity's own httpapi/orchestrate "composition root" packages, so
// it can keep checking old captures even as the gateway's own code
// changes shape), Violation, and the Rule registration type.
package rules

import "fmt"

// CaptureRecord is one line of request_captures/*.ndjson, decoded
// generically (map[string]interface{}) — it only needs to know the JSON
// shape internal/speccapture writes (see that package's capture.go/
// finish.go), never the gateway's own typed domain for the OUTER shape.
// Individual rules that need a typed view of one LEG's body (rules 10/11
// decode a leg into claude.Request/a JSON Schema string) do that decode
// themselves, from the raw bytes this type's helpers hand back.
type CaptureRecord map[string]interface{}

func (r CaptureRecord) rid() string {
	if v, ok := r["rid"]; ok {
		return fmt.Sprint(v)
	}
	return "?"
}

func (r CaptureRecord) timestamp() string {
	if v, ok := r["timestamp"].(string); ok {
		return v
	}
	return ""
}

func (r CaptureRecord) sessionID() string {
	s, _ := r["session_id"].(string)
	return s
}

func (r CaptureRecord) agentKey() string {
	s, _ := r["agent_key"].(string)
	return s
}

// snapshotBody returns the "body" field of a top-level leg
// ("claude_request", "gemini_request", "gemini_response",
// "claude_response"), or nil if that leg wasn't captured for this
// request, or was captured as a streamed {"chunks"/"events": [...]}
// accumulation instead of one complete body.
func (r CaptureRecord) snapshotBody(leg string) interface{} {
	leg2, ok := r[leg].(map[string]interface{})
	if !ok {
		return nil
	}
	return leg2["body"]
}

func (r CaptureRecord) snapshotHeaders(leg string) map[string]interface{} {
	leg2, ok := r[leg].(map[string]interface{})
	if !ok {
		return nil
	}
	h, _ := leg2["headers"].(map[string]interface{})
	return h
}

// streamedChunks returns the geminiResponse leg's streamed chunk
// accumulation ({"chunks": [...]}), for rules (12) that need the LAST
// usageMetadata across a streamed response rather than one complete
// body.
func (r CaptureRecord) streamedChunks() []interface{} {
	leg, ok := r["gemini_response"].(map[string]interface{})
	if !ok {
		return nil
	}
	chunks, _ := leg["chunks"].([]interface{})
	return chunks
}

// Violation is one broken invariant, found in one captured record (or,
// for the config/code-invariant rules 8-9 which never read a record at
// all, carrying a synthetic RID/Timestamp identifying the check itself
// rather than a request).
type Violation struct {
	Rule      string
	RID       string
	Timestamp string
	Source    string // which .ndjson file this came from, empty for 8/9
	Detail    string
}

func violation(rec CaptureRecord, detail string) Violation {
	return Violation{RID: rec.rid(), Timestamp: rec.timestamp(), Detail: detail}
}

// Rule is one named invariant. Check receives the FULL loaded corpus
// (every record speccheck read) rather than one record at a time — most
// rules (1-7, 10, 11) still loop over records internally exactly like
// v1's per-record Check did, but rules 8/9 are config/code invariants
// that ignore records entirely, and rule 12 is a genuine aggregate over
// the whole corpus (Part 7.6 #12: "≥95% of a day's traffic"). Unifying
// on one signature keeps cmd/speccheck's own run loop (main.go) simple —
// it never needs to know which rules are per-record vs aggregate.
type Rule struct {
	Name  string
	Check func(records []CaptureRecord) []Violation
}

// --- shared decode helpers, used by more than one rule file -----------

func asArray(v interface{}) []interface{} {
	a, _ := v.([]interface{})
	return a
}

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

func asString(v interface{}) string {
	s, _ := v.(string)
	return s
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
