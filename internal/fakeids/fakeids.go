// Package fakeids holds the fixed placeholder organization/workspace/
// account UUIDs this gateway hands back in spoofed Anthropic-CLI-facing
// responses (bootstrap, oauth profile/validate, usage headers) — not
// backed by a real Anthropic subscription, since this gateway has none.
//
// In v1 these lived as an unexported const block inside server.go, used
// directly from mitm.go's HandleBootstrapSpoof because v1 is one
// `package main` where every file can reach every other file's
// constants. internal/mitm (bootstrap spoofing) and the future
// internal/httpapi (oauth profile/validate, usage headers) have no other
// reason to depend on each other, so — same single-ownership rule as
// internal/sessionkey in this same migration phase — these three
// constants get their own tiny leaf package both import.
package fakeids

const (
	OrgID       = "0bad747f-0000-4000-a000-000000000000"
	WorkspaceID = "wrkspc_0000000000000000000000000"
	// An audit caught that several hardcoded CLI-spoof responses used
	// the SAME org UUID for the account/user object too — real Claude
	// clients can cache fetched objects keyed by UUID, so a client
	// fetching "account" and "organization" back to back saw identical
	// UUIDs for two conceptually different entities, risking one
	// overwriting the other's cached fields.
	AccountID = "0bad747f-0000-4000-a000-000000000001"
)
