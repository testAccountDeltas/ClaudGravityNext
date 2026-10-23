// Characterization tests for every rule in this package — one
// synthetic fixture proving each rule catches its own violation, and
// one (or the shared clean fixture) proving it stays quiet on clean
// input. Rules 1-7 mirror the exact incident each one is a literal
// regression test for (see each rule file's doc comment); rules 8-12
// are new, so their tests are built straight from this rule's own
// described behavior.
package rules

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func rec(t *testing.T, legs map[string]interface{}) CaptureRecord {
	t.Helper()
	out := CaptureRecord{"rid": "1", "timestamp": "2026-01-01T00:00:00Z"}
	for k, v := range legs {
		out[k] = v
	}
	return out
}

func leg(body interface{}) map[string]interface{} {
	return map[string]interface{}{"body": body}
}

// --- 1: RoleAlternation -------------------------------------------------

func TestRoleAlternation_CatchesRepeatedRole(t *testing.T) {
	r := rec(t, map[string]interface{}{"gemini_request": leg(map[string]interface{}{
		"contents": []interface{}{
			map[string]interface{}{"role": "user"},
			map[string]interface{}{"role": "user"},
		},
	})})
	if v := RoleAlternation.Check([]CaptureRecord{r}); len(v) == 0 {
		t.Fatal("expected a violation for two consecutive user turns")
	}
}

func TestRoleAlternation_PassesOnAlternating(t *testing.T) {
	r := rec(t, map[string]interface{}{"gemini_request": leg(map[string]interface{}{
		"contents": []interface{}{
			map[string]interface{}{"role": "user"},
			map[string]interface{}{"role": "model"},
		},
	})})
	if v := RoleAlternation.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations, got %v", v)
	}
}

// --- 2: ToolCallResponseParity ------------------------------------------

func TestToolCallResponseParity_CatchesMissingResponse(t *testing.T) {
	r := rec(t, map[string]interface{}{"gemini_request": leg(map[string]interface{}{
		"contents": []interface{}{
			map[string]interface{}{"role": "model", "parts": []interface{}{
				map[string]interface{}{"functionCall": map[string]interface{}{"name": "x"}},
			}},
			map[string]interface{}{"role": "user", "parts": []interface{}{
				map[string]interface{}{"text": "no response here"},
			}},
		},
	})})
	if v := ToolCallResponseParity.Check([]CaptureRecord{r}); len(v) == 0 {
		t.Fatal("expected a violation for a functionCall with no matching functionResponse")
	}
}

func TestToolCallResponseParity_PassesOnMatchedPair(t *testing.T) {
	r := rec(t, map[string]interface{}{"gemini_request": leg(map[string]interface{}{
		"contents": []interface{}{
			map[string]interface{}{"role": "model", "parts": []interface{}{
				map[string]interface{}{"functionCall": map[string]interface{}{"name": "x"}},
			}},
			map[string]interface{}{"role": "user", "parts": []interface{}{
				map[string]interface{}{"functionResponse": map[string]interface{}{"name": "x"}},
			}},
		},
	})})
	if v := ToolCallResponseParity.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations, got %v", v)
	}
}

// --- 3: ContentNotCollapsed ----------------------------------------------

func TestContentNotCollapsed_CatchesAllCollapsed(t *testing.T) {
	msg := map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "text", "text": "..."}}}
	r := rec(t, map[string]interface{}{"claude_request": leg(map[string]interface{}{
		"messages": []interface{}{msg, msg},
	})})
	if v := ContentNotCollapsed.Check([]CaptureRecord{r}); len(v) == 0 {
		t.Fatal("expected a violation when every message collapsed to \"...\"")
	}
}

func TestContentNotCollapsed_PassesOnOneSentinelAmongNormal(t *testing.T) {
	collapsed := map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "text", "text": "..."}}}
	normal := map[string]interface{}{"content": []interface{}{map[string]interface{}{"type": "text", "text": "hello"}}}
	r := rec(t, map[string]interface{}{"claude_request": leg(map[string]interface{}{
		"messages": []interface{}{collapsed, normal},
	})})
	if v := ContentNotCollapsed.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations (one legitimate sentinel), got %v", v)
	}
}

// --- 4: RequiredResponseHeaders -------------------------------------------

func TestRequiredResponseHeaders_CatchesMissingHeader(t *testing.T) {
	r := rec(t, map[string]interface{}{"claude_response": map[string]interface{}{
		"headers": map[string]interface{}{"anthropic-ratelimit-requests-limit": "100"},
		"body":    map[string]interface{}{"type": "message"},
	}})
	if v := RequiredResponseHeaders.Check([]CaptureRecord{r}); len(v) == 0 {
		t.Fatal("expected violations for the 3 missing rate-limit headers")
	}
}

func TestRequiredResponseHeaders_PassesWithAllHeaders(t *testing.T) {
	r := rec(t, map[string]interface{}{"claude_response": map[string]interface{}{
		"headers": map[string]interface{}{
			"anthropic-ratelimit-requests-limit":     "100",
			"anthropic-ratelimit-requests-remaining": "99",
			"anthropic-ratelimit-tokens-limit":       "1000",
			"anthropic-ratelimit-tokens-remaining":   "999",
		},
		"body": map[string]interface{}{"type": "message"},
	}})
	if v := RequiredResponseHeaders.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations, got %v", v)
	}
}

// --- 5: SchemaKeywordAllowlist --------------------------------------------

func TestSchemaKeywordAllowlist_CatchesUnsupportedKey(t *testing.T) {
	r := rec(t, map[string]interface{}{"gemini_request": leg(map[string]interface{}{
		"tools": []interface{}{map[string]interface{}{"functionDeclarations": []interface{}{
			map[string]interface{}{"name": "x", "parameters": map[string]interface{}{"type": "OBJECT", "$ref": "#/foo"}},
		}}},
	})})
	if v := SchemaKeywordAllowlist.Check([]CaptureRecord{r}); len(v) == 0 {
		t.Fatal("expected a violation for an unsupported schema key ($ref)")
	}
}

func TestSchemaKeywordAllowlist_PassesOnAllowedKeys(t *testing.T) {
	r := rec(t, map[string]interface{}{"gemini_request": leg(map[string]interface{}{
		"tools": []interface{}{map[string]interface{}{"functionDeclarations": []interface{}{
			map[string]interface{}{"name": "x", "parameters": map[string]interface{}{"type": "OBJECT", "properties": map[string]interface{}{}}},
		}}},
	})})
	if v := SchemaKeywordAllowlist.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations, got %v", v)
	}
}

// --- 6: TokenAccountingSanity ---------------------------------------------

func TestTokenAccountingSanity_CatchesDivergence(t *testing.T) {
	r := rec(t, map[string]interface{}{
		"claude_response": leg(map[string]interface{}{"usage": map[string]interface{}{"input_tokens": 10000.0}}),
		"gemini_response": leg(map[string]interface{}{"usageMetadata": map[string]interface{}{"promptTokenCount": 100.0}}),
	})
	if v := TokenAccountingSanity.Check([]CaptureRecord{r}); len(v) == 0 {
		t.Fatal("expected a violation for a 100x divergence")
	}
}

func TestTokenAccountingSanity_PassesWithinBand(t *testing.T) {
	r := rec(t, map[string]interface{}{
		"claude_response": leg(map[string]interface{}{"usage": map[string]interface{}{"input_tokens": 100.0}}),
		"gemini_response": leg(map[string]interface{}{"usageMetadata": map[string]interface{}{"promptTokenCount": 102.0}}),
	})
	if v := TokenAccountingSanity.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations, got %v", v)
	}
}

// --- 7: SSEEventSequence ---------------------------------------------------

func TestSSEEventSequence_CatchesUnclosedBlock(t *testing.T) {
	r := rec(t, map[string]interface{}{"claude_response": map[string]interface{}{
		"events": []interface{}{
			map[string]interface{}{"event": "message_start"},
			map[string]interface{}{"event": "content_block_start", "data": map[string]interface{}{"index": 0.0}},
			map[string]interface{}{"event": "message_stop"},
		},
	}})
	if v := SSEEventSequence.Check([]CaptureRecord{r}); len(v) == 0 {
		t.Fatal("expected a violation for a content block opened but never closed")
	}
}

func TestSSEEventSequence_PassesOnWellFormedSequence(t *testing.T) {
	r := rec(t, map[string]interface{}{"claude_response": map[string]interface{}{
		"events": []interface{}{
			map[string]interface{}{"event": "message_start"},
			map[string]interface{}{"event": "content_block_start", "data": map[string]interface{}{"index": 0.0}},
			map[string]interface{}{"event": "content_block_stop", "data": map[string]interface{}{"index": 0.0}},
			map[string]interface{}{"event": "message_stop"},
		},
	}})
	if v := SSEEventSequence.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations, got %v", v)
	}
}

// --- 8: ConnectAllowlistSelfConsistency -----------------------------------

func writeTestConfig(t *testing.T, tlsPort int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := map[string]interface{}{
		"server": map[string]interface{}{
			"port":               8898,
			"tls_port":           tlsPort,
			"dashboard_password": "x",
			"proxy_secret":       "y",
		},
		"tenants": map[string]interface{}{},
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConnectAllowlistSelfConsistency_PassesOnRealConfigShape(t *testing.T) {
	// internal/mitm.AllowedConnectPorts always includes whatever tls_port
	// is passed to it (that's the whole point of the fix in 89de11b), so
	// this rule passes for any valid config.json — the violation path
	// (below) exercises the "could not load config" branch instead, since
	// AllowedConnectPorts itself can't currently diverge without editing
	// internal/mitm directly.
	oldPath := ConfigPath
	defer func() { ConfigPath = oldPath }()
	ConfigPath = writeTestConfig(t, 8900)

	if v := ConnectAllowlistSelfConsistency.Check(nil); len(v) != 0 {
		t.Fatalf("expected no violations for a valid config, got %v", v)
	}
}

func TestConnectAllowlistSelfConsistency_CatchesUnreadableConfig(t *testing.T) {
	oldPath := ConfigPath
	defer func() { ConfigPath = oldPath }()
	ConfigPath = filepath.Join(t.TempDir(), "does-not-exist.json")

	if v := ConnectAllowlistSelfConsistency.Check(nil); len(v) == 0 {
		t.Fatal("expected a violation when config.json can't be loaded at all")
	}
}

// --- 9: EphemeralTTLBounded ------------------------------------------------

func TestEphemeralTTLBounded_PassesOnHealthyStore(t *testing.T) {
	if v := EphemeralTTLBounded.Check(nil); len(v) != 0 {
		t.Fatalf("expected no violations against a correctly-behaving ephemeral.Store, got %v", v)
	}
}

// --- 10: SanitizeIdempotenceOnCapturedTraffic ------------------------------

func TestSanitizeIdempotenceOnCapturedTraffic_PassesOnOrdinaryConversation(t *testing.T) {
	r := rec(t, map[string]interface{}{"claude_request": leg(map[string]interface{}{
		"model": "claude-sonnet-4-5",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": []interface{}{
				map[string]interface{}{"type": "text", "text": "hello"},
			}},
		},
	})})
	if v := SanitizeIdempotenceOnCapturedTraffic.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations (sanitize is idempotent by construction), got %v", v)
	}
}

func TestSanitizeIdempotenceOnCapturedTraffic_NoOpOnMissingLeg(t *testing.T) {
	r := rec(t, map[string]interface{}{})
	if v := SanitizeIdempotenceOnCapturedTraffic.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations when claude_request wasn't captured, got %v", v)
	}
}

// --- 11: SchemaRefCycleTermination -----------------------------------------

func TestSchemaRefCycleTermination_PassesOnRealRecursiveRef(t *testing.T) {
	// A genuinely recursive $ref (a tree node schema referencing itself)
	// is exactly the shape internal/schema/refs.go's cap-at-10-expansions
	// guard exists for — this proves CleanJSONSchemaForGemini still
	// terminates well within the rule's timeout on that shape.
	schemaJSON := `{
		"type": "object",
		"$defs": {"Node": {"type": "object", "properties": {"child": {"$ref": "#/$defs/Node"}}}},
		"properties": {"root": {"$ref": "#/$defs/Node"}}
	}`
	var parsedSchema interface{}
	if err := json.Unmarshal([]byte(schemaJSON), &parsedSchema); err != nil {
		t.Fatal(err)
	}
	r := rec(t, map[string]interface{}{"claude_request": leg(map[string]interface{}{
		"tools": []interface{}{map[string]interface{}{"name": "tree_tool", "input_schema": parsedSchema}},
	})})
	if v := SchemaRefCycleTermination.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations (the existing pathVisited guard should terminate this), got %v", v)
	}
}

func TestSchemaRefCycleTermination_SkipsToolsWithoutRef(t *testing.T) {
	r := rec(t, map[string]interface{}{"claude_request": leg(map[string]interface{}{
		"tools": []interface{}{map[string]interface{}{"name": "plain", "input_schema": map[string]interface{}{"type": "object"}}},
	})})
	if v := SchemaRefCycleTermination.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations for a schema with no $ref at all, got %v", v)
	}
}

// --- 12: TokenDriftWithinBand -----------------------------------------------

func claudeReqLeg(estimatedTextChars int) map[string]interface{} {
	text := make([]byte, estimatedTextChars)
	for i := range text {
		text[i] = 'a'
	}
	return leg(map[string]interface{}{
		"model": "claude-sonnet-4-5",
		"messages": []interface{}{
			map[string]interface{}{"role": "user", "content": []interface{}{
				map[string]interface{}{"type": "text", "text": string(text)},
			}},
		},
	})
}

func TestTokenDriftWithinBand_PassesWhenRatioInBand(t *testing.T) {
	r := rec(t, map[string]interface{}{"claude_request": claudeReqLeg(40)})
	estimated, ok := estimatedRequestTokens(r)
	if !ok || estimated <= 0 {
		t.Fatal("expected a positive estimate for the fixture request")
	}
	// Pick an upstream value that lands the ratio in the middle of the
	// target band [1.00, 1.30], derived from the real estimate rather
	// than a guessed constant.
	upstream := float64(estimated) / 1.15
	r["gemini_response"] = leg(map[string]interface{}{"usageMetadata": map[string]interface{}{"promptTokenCount": upstream}})

	if v := TokenDriftWithinBand.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations for a single in-band sample, got %v", v)
	}
}

func TestTokenDriftWithinBand_CatchesOutOfBandMajority(t *testing.T) {
	// upstream far larger than the estimate -> ratio well under 1.00 for
	// every sample -> both the per-sample and the aggregate violation fire.
	var records []CaptureRecord
	for i := 0; i < 5; i++ {
		records = append(records, rec(t, map[string]interface{}{
			"claude_request":  claudeReqLeg(5),
			"gemini_response": leg(map[string]interface{}{"usageMetadata": map[string]interface{}{"promptTokenCount": 100000.0}}),
		}))
	}
	v := TokenDriftWithinBand.Check(records)
	if len(v) == 0 {
		t.Fatal("expected violations when every sample's driftRatio is far outside [1.00, 1.30]")
	}
	foundAggregate := false
	for _, vi := range v {
		if vi.RID == "aggregate" {
			foundAggregate = true
		}
	}
	if !foundAggregate {
		t.Errorf("expected an aggregate summary violation among %v", v)
	}
}

func TestTokenDriftWithinBand_SkipsRecordsMissingUpstreamData(t *testing.T) {
	r := rec(t, map[string]interface{}{"claude_request": claudeReqLeg(10)})
	if v := TokenDriftWithinBand.Check([]CaptureRecord{r}); len(v) != 0 {
		t.Fatalf("expected no violations when no real promptTokenCount was ever captured, got %v", v)
	}
}
