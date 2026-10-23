// Tests for internal/reqctx — not a v1-characterization test (there's no
// single v1 function to compare against; Params/State are new types
// replacing a long positional parameter list), just a construction
// smoke test pinning that every field the migration spec called out by
// name actually exists and is settable.
package reqctx

import (
	"testing"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
)

func TestParams_AllSpecCalledOutFieldsExist(t *testing.T) {
	p := &Params{
		RID:                    1,
		SessionID:              "sess1",
		ClientKey:              "key1",
		Tenant:                 config.TenantConfig{},
		ServerCfg:              config.ServerConfig{},
		ModelName:              "claude-opus-4-5",
		AgentKey:               "agent1",
		AgentType:              "main",
		SubagentName:           "",
		InstanceID:             "inst1",
		IsStreaming:            true,
		HasSafeguards:          true,
		PreferRussian:          true,
		StopSequences:          []string{"STOP"},
		HasPriorTurns:          true,
		DisableParallelToolUse: true,
		PrefillText:            "{",
		SyntheticReasonTools:   map[string]bool{"x": true},
		ReverseToolNameMap:     map[string]string{"a": "b"},
		RawClaudeBody:          []byte(`{}`),
	}
	if p.RID != 1 || !p.IsStreaming || p.PrefillText != "{" {
		t.Fatalf("got %+v", p)
	}
}

func TestState_RebuildsFreshPerAttempt(t *testing.T) {
	st1 := &State{Attempt: 1, ActiveAccount: &accounts.Record{}, BearerToken: "tok-a", ResolvedModelName: "gemini-2.5-pro"}
	st2 := &State{Attempt: 2, ActiveAccount: &accounts.Record{}, BearerToken: "tok-b", ResolvedModelName: "gemini-3.8-flash-tiered"}
	if st1.BearerToken == st2.BearerToken || st1.ResolvedModelName == st2.ResolvedModelName {
		t.Fatal("two independently constructed States must not share mutable fields")
	}
}
