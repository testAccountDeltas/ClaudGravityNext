// Package gemini is a NEW typed domain for Google's generateContent API
// (Gemini/CloudCode/Antigravity) — not a port of any single v1 file,
// called for by ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md
// Part 5.2, symmetric with internal/protocol/claude (see that package's
// doc comment for the design rationale). Grounded in the exact field
// paths superrelay (v1)'s translator.go/sse.go/server.go read and write
// (grepped before writing this, not invented from docs alone).
package gemini

type Role string

const (
	RoleUser  Role = "user"
	RoleModel Role = "model"
)

// Content is one turn.
type Content struct {
	Role  Role   `json:"role,omitempty"`
	Parts []Part `json:"parts"`
}
