// Rule 13 — TranslateStructuralParity. NOT in the original Part 7.6
// table (that one stops at 12) — added after a manual investigation
// this same session found cmd/shadowcompare's "100% divergence" result
// on real traffic was entirely leaf-value noise (speccapture's own
// text/signature redaction producing a placeholder on one side and
// internal/translate's fresh, unredacted computation on the other —
// never a real bug) with zero STRUCTURAL divergence (no field silently
// missing, no extra field, no array-length mismatch, no type mismatch)
// across every record checked by hand. That manual check is exactly the
// kind of thing speccheck exists to make automatic and continuous
// instead of a one-off exercise (see AGENT_HANDOFF_NOTES.md's
// "speccheck — run this BEFORE trusting a new audit's claims" section)
// — this rule runs the SAME comparison (internal/jsondiff, the package
// extracted from cmd/shadowcompare for this exact reuse) on every
// record, but reports ONLY structural mismatches as violations. A leaf-
// value mismatch is never reported here: that's expected noise from
// redaction and real, intentional translate changes landing between
// when a capture was written and when this rule runs — see
// ROADMAP_PROGRESS.md's Phase 5 write-up for two concrete examples where
// v1 itself changed behavior (commits 75f473e, 398384d) after a capture
// was taken, which this rule correctly does NOT flag.
package rules

import (
	"encoding/json"
	"fmt"

	"claudgravity/internal/config"
	"claudgravity/internal/jsondiff"
	"claudgravity/internal/translate"
)

func checkTranslateStructuralParity(rec CaptureRecord) []Violation {
	claudeBody := rec.snapshotBody("claude_request")
	geminiBody := rec.snapshotBody("gemini_request")
	if claudeBody == nil || geminiBody == nil {
		return nil
	}
	claudeRaw, err := json.Marshal(claudeBody)
	if err != nil {
		return nil
	}

	agentKey := rec.agentKey()
	if agentKey == "" {
		agentKey = "main"
	}
	// Same best-effort tenant cmd/shadowcompare uses: the real per-tenant
	// config isn't in the capture, and StripSearchTools:true/
	// ThinkingBudget:-1 is this gateway's own shipped default (config.go's
	// CreateAPIKey), not a guess.
	tenant := config.TenantConfig{
		DefaultModel:     "gemini-3.8-flash-tiered",
		ThinkingBudget:   -1,
		StripSearchTools: true,
	}

	result, err := translate.ConvertClaudeRequestToGemini(claudeRaw, tenant, "", agentKey, rec.sessionID(), "", true)
	if err != nil {
		// A translate error on real, previously-successfully-translated
		// traffic (v1 produced a real gemini_request for this exact
		// input, or this record wouldn't have one) IS itself a
		// structural regression — report it rather than silently
		// skipping like the nil-body guard above does for genuinely
		// absent data.
		return []Violation{violation(rec, fmt.Sprintf("internal/translate.ConvertClaudeRequestToGemini failed on a captured request v1 itself successfully translated: %v", err))}
	}

	var v2Body interface{}
	if err := json.Unmarshal(result.GeminiBody, &v2Body); err != nil {
		return nil
	}
	v1Body := geminiBody

	v1Redacted := jsondiff.Redact(v1Body)
	v2Redacted := jsondiff.Redact(v2Body)

	var paths []string
	jsondiff.Diff("", v1Redacted, v2Redacted, &paths)

	var out []Violation
	for _, p := range paths {
		if jsondiff.IsStructural(p) {
			out = append(out, violation(rec, "structural diff vs v1's own captured gemini_request: "+p))
		}
	}
	return out
}

var TranslateStructuralParity = Rule{
	Name: "TranslateStructuralParity",
	Check: func(records []CaptureRecord) []Violation {
		var out []Violation
		for _, rec := range records {
			out = append(out, checkTranslateStructuralParity(rec)...)
		}
		return out
	},
}
