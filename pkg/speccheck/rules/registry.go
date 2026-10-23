// registry.go is the single point where every rule in this package is
// collected into the list cmd/speccheck actually runs — adding a new
// invariant is a new file (this package's established per-rule
// convention) plus one line here, per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 4.3's own
// description of this file's purpose.
package rules

// AllRules is every registered invariant, numbered per Part 7.6. Rules
// 1-7 are ported from superrelay (v1)'s cmd/speccheck/rules.go; rules
// 8-12 are new (see each rule's own file for why).
var AllRules = []Rule{
	RoleAlternation,                      // 1
	ToolCallResponseParity,               // 2
	ContentNotCollapsed,                  // 3
	RequiredResponseHeaders,              // 4
	SchemaKeywordAllowlist,               // 5
	TokenAccountingSanity,                // 6
	SSEEventSequence,                     // 7
	ConnectAllowlistSelfConsistency,      // 8
	EphemeralTTLBounded,                  // 9
	SanitizeIdempotenceOnCapturedTraffic, // 10
	SchemaRefCycleTermination,            // 11
	TokenDriftWithinBand,                 // 12
	TranslateStructuralParity,            // 13 — not in the original Part 7.6 table; added same session after a manual investigation found this exact check valuable enough to automate (see translateparity.go's own doc comment)
}
