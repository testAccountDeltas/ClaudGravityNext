package tokens

import (
	"encoding/json"
	"strings"
	"testing"

	"claudgravity/internal/protocol/claude"
)

// The estimate is scaled by estimateCalibration so that it sits in the project's
// target band [1.00, 1.30] of Gemini's own prompt token count instead of just
// below it (see WeightedRequestTokensFor).
func TestWeightedRequestTokens_AppliesTheDriftCalibration(t *testing.T) {
	var req claude.Request
	if err := json.Unmarshal([]byte(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"`+strings.Repeat("hello world ", 400)+`"}]}`), &req); err != nil {
		t.Fatal(err)
	}
	raw := weightedSystemTokens(req.System) + weightedMessagesTokens(req.Messages, defaultProfile) + weightedToolsTokens(req.Tools)
	if got, want := WeightedRequestTokens(req), int(float64(raw)*estimateCalibration); got != want || got <= raw {
		t.Fatalf("estimate = %d, want %d (uncalibrated %d)", got, want, raw)
	}
	if estimateCalibration < 1.0 || estimateCalibration > 1.15 {
		t.Fatalf("calibration %v is outside the sane range", estimateCalibration)
	}
}
