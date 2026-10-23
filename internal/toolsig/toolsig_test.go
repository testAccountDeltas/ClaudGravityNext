package toolsig

import (
	"testing"
)

func TestSubagentWithColonsInSessionID(t *testing.T) {
	parentSession := "tenantPrefix:sess_parent_123"
	subagentA := parentSession + ":sub_explore_1"

	SetSessionSignature(parentSession, "sig_parent")

	if got := GetSessionSignature(subagentA); got != "sig_parent" {
		t.Fatalf("subagentA expected fallback to parent signature %q, got %q", "sig_parent", got)
	}
}

func TestSubagentSignatureIsolationAndFallback(t *testing.T) {
	parentSession := "sess_parent_123"
	subagentA := parentSession + ":sub_explore_1"
	subagentB := parentSession + ":sub_explore_2"

	// 1. Parent session records a signature
	SetSessionSignature(parentSession, "sig_parent")

	// 2. Both subagents fall back to parent signature before recording their own
	if got := GetSessionSignature(subagentA); got != "sig_parent" {
		t.Fatalf("subagentA expected fallback to parent signature, got %q", got)
	}
	if got := GetSessionSignature(subagentB); got != "sig_parent" {
		t.Fatalf("subagentB expected fallback to parent signature, got %q", got)
	}

	// 3. Subagent A records its own signature
	SetSessionSignature(subagentA, "sig_subagent_a")

	// 4. Subagent A sees its own signature; Subagent B still falls back to parent and is NOT contaminated
	if got := GetSessionSignature(subagentA); got != "sig_subagent_a" {
		t.Fatalf("subagentA expected own signature, got %q", got)
	}
	if got := GetSessionSignature(subagentB); got != "sig_parent" {
		t.Fatalf("subagentB expected parent signature (not contaminated by subagentA), got %q", got)
	}

	// 5. Subagent B records its own signature
	SetSessionSignature(subagentB, "sig_subagent_b")
	if got := GetSessionSignature(subagentB); got != "sig_subagent_b" {
		t.Fatalf("subagentB expected own signature, got %q", got)
	}
	if got := GetSessionSignature(subagentA); got != "sig_subagent_a" {
		t.Fatalf("subagentA signature must remain intact, got %q", got)
	}
}
