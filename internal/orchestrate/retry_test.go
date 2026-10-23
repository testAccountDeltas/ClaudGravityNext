package orchestrate

import (
	"strings"
	"testing"
)

func TestSessionBump_DefaultsToZero(t *testing.T) {
	if got := getSessionBump("never-seen-session"); got != 0 {
		t.Fatalf("expected 0 for an unknown session, got %d", got)
	}
}

func TestSessionBump_RoundTrip(t *testing.T) {
	setSessionBump("sess-a", 2)
	if got := getSessionBump("sess-a"); got != 2 {
		t.Fatalf("expected the bump just set to read back, got %d", got)
	}
}

func TestSessionBump_EmptySessionIDIsNoOp(t *testing.T) {
	setSessionBump("", 5)
	if got := getSessionBump(""); got != 0 {
		t.Fatalf("expected an empty session id to never be stored, got %d", got)
	}
}

func TestSessionBump_PersistsAcrossSessionScopedKeys(t *testing.T) {
	// Regression for the real v1 incident this exists to fix: a bump
	// discovered on one request must be visible to the NEXT request for
	// the same (sub)agent-scoped session key, not reset to 0.
	setSessionBump("sess-b:sub-1", 1)
	setSessionBump("sess-b:sub-2", 0)
	if got := getSessionBump("sess-b:sub-1"); got != 1 {
		t.Fatalf("expected sub-1's own bump to survive independently, got %d", got)
	}
	if got := getSessionBump("sess-b:sub-2"); got != 0 {
		t.Fatalf("expected sub-2's bump to stay at 0, unaffected by sub-1's, got %d", got)
	}
}

func TestReplaceThoughtSignaturesWithSkipValidator(t *testing.T) {
	input := []byte(`{"contents":[{"role":"model","parts":[{"thought":true,"text":"t1","thoughtSignature":"sig1"},{"functionCall":{"name":"bash","args":{}},"thoughtSignature":"sig2"},{"functionCall":{"name":"edit","args":{},"thoughtSignature":"sig3"}}]},{"role":"model","parts":[{"thought":true,"text":"t2","thoughtSignature":"skip_thought_signature_validator"}]}]}`)
	output := replaceThoughtSignaturesWithSkipValidator(input)
	outStr := string(output)

	// Check if all non-skip signatures were replaced with skip_thought_signature_validator
	if strings.Contains(outStr, "sig1") || strings.Contains(outStr, "sig2") || strings.Contains(outStr, "sig3") {
		t.Fatalf("expected all signatures to be replaced, but found originals in: %s", outStr)
	}
	// Verify that skip_thought_signature_validator is preserved
	if !strings.Contains(outStr, `"thoughtSignature":"skip_thought_signature_validator"`) {
		t.Fatalf("expected skip_thought_signature_validator to be present in: %s", outStr)
	}
}

func TestIsThoughtSignatureMismatchError(t *testing.T) {
	tests := []struct {
		status   int
		errMsg   string
		expected bool
	}{
		{400, "Thought signature does not match content", true},
		{400, "thought_signature does not match content", true},
		{400, "Invalid Content.parts[0]: thoughtSignature mismatch", true},
		{400, "THOUGHT_SIGNATURE validation failed", true},
		{400, "Invalid field 'unknown_param'", false},
		{429, "Rate limit exceeded for thoughtSignature", false},
		{500, "internal error", false},
		{400, "", false},
	}

	for _, tc := range tests {
		var err error
		if tc.errMsg != "" {
			err = &testErr{tc.errMsg}
		}
		if got := isThoughtSignatureMismatchError(tc.status, err); got != tc.expected {
			t.Errorf("isThoughtSignatureMismatchError(%d, %q) = %v; want %v", tc.status, tc.errMsg, got, tc.expected)
		}
	}
}

type testErr struct{ msg string }

func (e *testErr) Error() string { return e.msg }
