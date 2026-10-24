package translate

import (
	"errors"
	"testing"

	"claudgravity/internal/config"
)

// Captured at package init, before any test can call
// SetAllowHistoryTruncation.
var initialAllowHistoryTruncation = allowHistoryTruncation.Load()

func setTruncationForTest(t *testing.T, v bool) {
	t.Helper()
	prev := allowHistoryTruncation.Load()
	SetAllowHistoryTruncation(v)
	t.Cleanup(func() { SetAllowHistoryTruncation(prev) })
}

// Regression: allow_history_truncation defaults to off. Reverting the
// default (or the gate in ConvertClaudeRequestToGemini) silently cuts a
// long history instead of reporting the context-exceeded error.
func TestRegress_AllowHistoryTruncationDefaultOff(t *testing.T) {
	if initialAllowHistoryTruncation {
		t.Fatal("allow_history_truncation must default to false")
	}
}

func TestRegress_OverWindowHistoryIsRejectedNotCut(t *testing.T) {
	setTruncationForTest(t, false)
	body := overflowBody(t, 40, 400_000)
	res, err := ConvertClaudeRequestToGemini(body, config.TenantConfig{DefaultModel: "gemini-3.8-flash"}, "", "main", "sess-regress-off", "", true)
	var of *ContextOverflowError
	if !errors.As(err, &of) {
		t.Fatalf("want ContextOverflowError with truncation off, got err=%v shrunk=%v", err, res.WasShrunk)
	}
	if of.Estimated <= of.Limit {
		t.Fatalf("estimate %d must exceed limit %d", of.Estimated, of.Limit)
	}
}

func TestRegress_OverWindowHistoryIsCutWhenOptedIn(t *testing.T) {
	setTruncationForTest(t, true)
	body := overflowBody(t, 40, 400_000)
	res, err := ConvertClaudeRequestToGemini(body, config.TenantConfig{DefaultModel: "gemini-3.8-flash"}, "", "main", "sess-regress-on", "", true)
	if err != nil {
		t.Fatalf("with the flag on the oldest turns must be dropped to fit, got %v", err)
	}
	if !res.WasShrunk {
		t.Fatal("truncation must be reported via WasShrunk")
	}
	cs := assertGeminiShape(t, res.GeminiBody)
	if len(cs) >= 41 {
		t.Fatalf("history not cut: %d contents", len(cs))
	}
}
