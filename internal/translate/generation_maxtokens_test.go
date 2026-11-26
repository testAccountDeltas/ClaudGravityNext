package translate

import (
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts"
)

func withCatalog(t *testing.T, models ...accounts.AvailableModelInfo) {
	t.Helper()
	t.Cleanup(accounts.SetCatalogForTest(models))
}

func TestApplyGenerationConfig_NoThinking_MaxOutputEqualsMaxTokens(t *testing.T) {
	withCatalog(t, accounts.AvailableModelInfo{ID: "gemini-x", Thinking: true, MaxOutput: 65536})
	out, _ := applyGenerationConfig("{}", gjson.Parse(`{"max_tokens":32}`), "gemini-x", 0, true)
	if got := gjson.Get(out, "generationConfig.maxOutputTokens").Int(); got != 32 {
		t.Fatalf("maxOutputTokens=%d, want 32: %s", got, out)
	}
	if gjson.Get(out, "generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts must be false: %s", out)
	}
	if gjson.Get(out, "generationConfig.thinkingConfig.thinkingBudget").Int() != 0 {
		t.Fatalf("thinkingBudget must be 0: %s", out)
	}
}

func TestApplyGenerationConfig_NoThinking_ModelCannotDisable_UsesFloorAndAllowance(t *testing.T) {
	withCatalog(t, accounts.AvailableModelInfo{ID: "gemini-3x", Thinking: true, MaxOutput: 65536, MinThinkingBudget: 128})
	out, budget := applyGenerationConfig("{}", gjson.Parse(`{"max_tokens":32}`), "gemini-3x", 0, true)
	if budget != 0 {
		t.Fatalf("returned budget must stay 0 (client has no thinking), got %d", budget)
	}
	if got := gjson.Get(out, "generationConfig.thinkingConfig.thinkingBudget").Int(); got != 128 {
		t.Fatalf("thinkingBudget=%d, want model floor 128", got)
	}
	if gjson.Get(out, "generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts must be false: %s", out)
	}
	if got := gjson.Get(out, "generationConfig.maxOutputTokens").Int(); got != 32+InternalReasoningAllowance {
		t.Fatalf("maxOutputTokens=%d, want %d (max_tokens + internal allowance)", got, 32+InternalReasoningAllowance)
	}
}

func TestApplyGenerationConfig_NoThinking_AllowanceNeverBelowFloorAndClampedToCeiling(t *testing.T) {
	withCatalog(t,
		accounts.AvailableModelInfo{ID: "big-floor", Thinking: true, MaxOutput: 65536, MinThinkingBudget: 2048},
		accounts.AvailableModelInfo{ID: "small-ceiling", Thinking: true, MaxOutput: 600, MinThinkingBudget: 128})
	out, _ := applyGenerationConfig("{}", gjson.Parse(`{"max_tokens":100}`), "big-floor", 0, true)
	if got := gjson.Get(out, "generationConfig.maxOutputTokens").Int(); got != 100+2048 {
		t.Fatalf("allowance must not drop below the model floor: maxOutputTokens=%d, want 2148", got)
	}
	if got := gjson.Get(out, "generationConfig.thinkingConfig.thinkingBudget").Int(); got != 2048 {
		t.Fatalf("thinkingBudget=%d, want floor 2048", got)
	}
	out, _ = applyGenerationConfig("{}", gjson.Parse(`{"max_tokens":500}`), "small-ceiling", 0, true)
	if got := gjson.Get(out, "generationConfig.maxOutputTokens").Int(); got != 600 {
		t.Fatalf("maxOutputTokens=%d, want clamp to model ceiling 600", got)
	}
}

func TestBumpOutputAllowance_AddsOneAllowanceClampedToCeiling(t *testing.T) {
	withCatalog(t, accounts.AvailableModelInfo{ID: "m", Thinking: true, MaxOutput: 3000})
	body := []byte(`{"generationConfig":{"maxOutputTokens":1500}}`)
	nb, ok := BumpOutputAllowance(body, "m")
	if !ok || gjson.GetBytes(nb, "generationConfig.maxOutputTokens").Int() != 1500+InternalReasoningAllowance {
		t.Fatalf("ok=%v body=%s", ok, nb)
	}
	nb, ok = BumpOutputAllowance(nb, "m")
	if !ok || gjson.GetBytes(nb, "generationConfig.maxOutputTokens").Int() != 3000 {
		t.Fatalf("second bump must clamp to ceiling 3000: ok=%v body=%s", ok, nb)
	}
	if _, ok = BumpOutputAllowance(nb, "m"); ok {
		t.Fatal("body at the ceiling cannot be widened")
	}
}

func TestApplyGenerationConfig_ThinkingEnabled_AddsBudgetToMaxOutput(t *testing.T) {
	withCatalog(t, accounts.AvailableModelInfo{ID: "gemini-x", Thinking: true, MaxOutput: 65536})
	out, _ := applyGenerationConfig("{}", gjson.Parse(`{"max_tokens":4000}`), "gemini-x", 2000, true)
	if got := gjson.Get(out, "generationConfig.maxOutputTokens").Int(); got != 6000 {
		t.Fatalf("maxOutputTokens=%d, want 6000", got)
	}
	if !gjson.Get(out, "generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts must be true when client enabled thinking: %s", out)
	}
}

func TestApplyGenerationConfig_Gemini4Hydrogen_AntigravityVsPublicAPI(t *testing.T) {
	withCatalog(t, accounts.AvailableModelInfo{
		ID:                "gemini-4-hydrogen-tiered-xhigh",
		Thinking:          true,
		MaxOutput:         262144,
		MinThinkingBudget: 32,
	})

	// Antigravity path uses thinkingBudget: -1 (ModeAuto) and full catalog MaxOutput.
	outAG, _ := applyGenerationConfig("{}", gjson.Parse(`{"max_tokens":4096,"temperature":0.2}`), "gemini-4-hydrogen-tiered-xhigh", -1, true)
	if got := gjson.Get(outAG, "generationConfig.thinkingConfig.thinkingBudget").Int(); got != -1 {
		t.Fatalf("Antigravity thinkingBudget=%d, want -1: %s", got, outAG)
	}
	if got := gjson.Get(outAG, "generationConfig.maxOutputTokens").Int(); got != 262144 {
		t.Fatalf("Antigravity maxOutputTokens=%d, want 262144: %s", got, outAG)
	}

	// Disabled thinking on Antigravity respects Hydrogen's minThinkingBudget=32 floor.
	outFloor, _ := applyGenerationConfig("{}", gjson.Parse(`{"max_tokens":100}`), "gemini-4-hydrogen-tiered-xhigh", 0, true)
	if got := gjson.Get(outFloor, "generationConfig.thinkingConfig.thinkingBudget").Int(); got != 32 {
		t.Fatalf("Antigravity disabled floor thinkingBudget=%d, want 32: %s", got, outFloor)
	}
	if gjson.Get(outFloor, "generationConfig.thinkingConfig.includeThoughts").Bool() {
		t.Fatalf("includeThoughts must be false when budget==0: %s", outFloor)
	}

	// Public Gemini Developer API (!isAntigravity) uses thinkingLevel and omits
	// thinkingBudget and sampling overrides.
	outPub, _ := applyGenerationConfig("{}", gjson.Parse(`{"max_tokens":4096,"temperature":0.2,"top_p":0.9,"top_k":20}`), "gemini-4-hydrogen-tiered-xhigh", -1, false)
	if got := gjson.Get(outPub, "generationConfig.thinkingConfig.thinkingLevel").String(); got != "xhigh" {
		t.Fatalf("public thinkingLevel=%q, want xhigh: %s", got, outPub)
	}
	if gjson.Get(outPub, "generationConfig.thinkingConfig.thinkingBudget").Exists() {
		t.Fatalf("public Gemini 4 must not set thinkingBudget: %s", outPub)
	}
	for _, k := range []string{"temperature", "topP", "topK"} {
		if gjson.Get(outPub, "generationConfig."+k).Exists() {
			t.Fatalf("public Gemini 4 must omit sampling override %q: %s", k, outPub)
		}
	}

	// Explicit/max thinkingBudget above Google's 65535 limit must clamp cleanly.
	outMax, clampedBudget := applyGenerationConfig("{}", gjson.Parse(`{"max_tokens":128000}`), "gemini-4-hydrogen-tiered-xhigh", 65536, true)
	if clampedBudget != 65535 || gjson.Get(outMax, "generationConfig.thinkingConfig.thinkingBudget").Int() != 65535 {
		t.Fatalf("thinkingBudget=%d returned=%d, want 65535: %s", gjson.Get(outMax, "generationConfig.thinkingConfig.thinkingBudget").Int(), clampedBudget, outMax)
	}
}
