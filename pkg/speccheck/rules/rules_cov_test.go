package rules

import (
	"encoding/json"
	"strings"
	"testing"

	"claudgravity/internal/config"
	"claudgravity/internal/translate"
)

func ev(name string, idx ...float64) map[string]interface{} {
	m := map[string]interface{}{"event": name}
	if len(idx) > 0 {
		m["data"] = map[string]interface{}{"index": idx[0]}
	}
	return m
}

func sseRec(t *testing.T, events ...interface{}) CaptureRecord {
	return rec(t, map[string]interface{}{"claude_response": map[string]interface{}{"events": events}})
}

func hasDetail(vs []Violation, frag string) bool {
	for _, v := range vs {
		if strings.Contains(v.Detail, frag) {
			return true
		}
	}
	return false
}

func TestCov_RecordHelpers(t *testing.T) {
	r := CaptureRecord{"rid": 5.0, "timestamp": "ts", "session_id": "s", "agent_key": "sub:x"}
	if r.rid() != "5" || r.timestamp() != "ts" || r.sessionID() != "s" || r.agentKey() != "sub:x" {
		t.Fatalf("%v %v %v %v", r.rid(), r.timestamp(), r.sessionID(), r.agentKey())
	}
	empty := CaptureRecord{"timestamp": 3}
	if empty.rid() != "?" || empty.timestamp() != "" || empty.sessionID() != "" || empty.agentKey() != "" {
		t.Fatal("missing/wrongly-typed fields must degrade to defaults")
	}
	if empty.snapshotBody("claude_request") != nil || empty.snapshotHeaders("claude_request") != nil || empty.streamedChunks() != nil {
		t.Fatal("absent legs -> nil")
	}
	r["claude_request"] = map[string]interface{}{"body": "b", "headers": map[string]interface{}{"h": "v"}}
	if r.snapshotBody("claude_request") != "b" || r.snapshotHeaders("claude_request")["h"] != "v" {
		t.Fatal("leg accessors")
	}
	r["gemini_response"] = map[string]interface{}{"chunks": []interface{}{1, 2}}
	if len(r.streamedChunks()) != 2 {
		t.Fatal("chunks")
	}
	r["gemini_response"] = map[string]interface{}{"chunks": "bad"}
	if r.streamedChunks() != nil {
		t.Fatal("non-array chunks -> nil")
	}
	if asArray("x") != nil || asMap(3) != nil || asString(1) != "" {
		t.Fatal("as* helpers")
	}
	v := violation(r, "d")
	if v.RID != "5" || v.Timestamp != "ts" || v.Detail != "d" {
		t.Fatalf("violation: %+v", v)
	}
}

func TestCov_EqualFoldASCII(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"Content-Type", "content-type", true}, {"ABC", "abd", false}, {"a", "ab", false}, {"", "", true}, {"x-Y", "X-y", true}, {"é", "É", false},
	}
	for _, c := range cases {
		if equalFoldASCII(c.a, c.b) != c.want {
			t.Errorf("equalFoldASCII(%q,%q) != %v", c.a, c.b, c.want)
		}
	}
}

func TestCov_SSESequenceViolations(t *testing.T) {
	// Leading pings are tolerated; wrong first event and wrong last event reported.
	v := checkSSEEventSequence(sseRec(t, ev("ping"), ev("content_block_start", 0), ev("content_block_stop", 0), ev("message_delta")))
	if !hasDetail(v, `started with "content_block_start"`) || !hasDetail(v, `ended with "message_delta"`) {
		t.Fatalf("%v", v)
	}
	// Reopened and never-opened blocks.
	v = checkSSEEventSequence(sseRec(t, ev("message_start"), ev("content_block_start", 1), ev("content_block_start", 1), ev("content_block_stop", 2), ev("message_stop")))
	if !hasDetail(v, "reopened already-open index 1") || !hasDetail(v, "closed index 2 that was never opened") || !hasDetail(v, "index 1 opened but never closed") {
		t.Fatalf("%v", v)
	}
	if v := checkSSEEventSequence(sseRec(t, ev("ping"))); len(v) != 1 || !hasDetail(v, `ended with "ping"`) {
		t.Fatalf("ping-only stream never terminated: %v", v)
	}
	// Error-terminated streams, ping-only streams and non-streaming bodies are fine.
	for _, r := range []CaptureRecord{
		sseRec(t, ev("ping"), ev("error")),
		sseRec(t, ev("message_start"), ev("error")),
		sseRec(t),
		rec(t, map[string]interface{}{"claude_response": leg("x")}),
		rec(t, nil),
	} {
		if v := checkSSEEventSequence(r); len(v) != 0 {
			t.Fatalf("unexpected %v", v)
		}
	}
}

func TestCov_SchemaKeywordsNestedWalk(t *testing.T) {
	params := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"a": map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string", "pattern": "x"}},
			"b": map[string]interface{}{"type": "string", "default": "d"},
		},
		"additionalProperties": false,
	}
	r := rec(t, map[string]interface{}{"gemini_request": leg(map[string]interface{}{
		"tools": []interface{}{map[string]interface{}{"functionDeclarations": []interface{}{
			map[string]interface{}{"name": "tool1", "parameters": params},
		}}},
	})})
	v := checkSchemaKeywordAllowlist(r)
	if len(v) != 3 || !hasDetail(v, `"additionalProperties"`) || !hasDetail(v, `"pattern"`) || !hasDetail(v, `"default"`) || !hasDetail(v, `tool "tool1"`) {
		t.Fatalf("%v", v)
	}
	var out []Violation
	walkSchemaKeys(nil, "t", &out, r)
	if len(out) != 0 {
		t.Fatal("nil node")
	}
	if len(checkSchemaKeywordAllowlist(rec(t, nil))) != 0 {
		t.Fatal("no gemini_request")
	}
}

func claudeReqBody() map[string]interface{} {
	return map[string]interface{}{
		"model":      "claude-sonnet-4-5",
		"max_tokens": 100.0,
		"messages":   []interface{}{map[string]interface{}{"role": "user", "content": "hello there, how are you today?"}},
	}
}

func TestCov_TokenDrift(t *testing.T) {
	est, ok := estimatedRequestTokens(rec(t, map[string]interface{}{"claude_request": leg(claudeReqBody())}))
	if !ok || est <= 0 {
		t.Fatalf("estimate: %d %v", est, ok)
	}
	if _, ok := estimatedRequestTokens(rec(t, nil)); ok {
		t.Fatal("no request -> not ok")
	}
	if _, ok := estimatedRequestTokens(rec(t, map[string]interface{}{"claude_request": leg("str")})); ok {
		t.Fatal("non-object body -> not ok")
	}
	if _, ok := estimatedRequestTokens(rec(t, map[string]interface{}{"claude_request": leg(map[string]interface{}{"f": func() {}})})); ok {
		t.Fatal("unmarshalable -> not ok")
	}

	// upstreamPromptTokens: complete body, streamed chunks (last non-zero wins), none.
	full := rec(t, map[string]interface{}{"gemini_response": leg(map[string]interface{}{"usageMetadata": map[string]interface{}{"promptTokenCount": 40.0}})})
	if n, ok := upstreamPromptTokens(full); !ok || n != 40 {
		t.Fatal("full body")
	}
	streamed := rec(t, map[string]interface{}{"gemini_response": map[string]interface{}{"chunks": []interface{}{
		map[string]interface{}{"usageMetadata": map[string]interface{}{"promptTokenCount": 11.0}},
		map[string]interface{}{"usageMetadata": map[string]interface{}{"promptTokenCount": 22.0}},
		"junk",
		map[string]interface{}{"usageMetadata": map[string]interface{}{"promptTokenCount": 0.0}},
	}}})
	if n, ok := upstreamPromptTokens(streamed); !ok || n != 22 {
		t.Fatalf("streamed: %v %v", n, ok)
	}
	if _, ok := upstreamPromptTokens(rec(t, nil)); ok {
		t.Fatal("none")
	}

	mk := func(upstream float64) CaptureRecord {
		return rec(t, map[string]interface{}{
			"claude_request":  leg(claudeReqBody()),
			"gemini_response": leg(map[string]interface{}{"usageMetadata": map[string]interface{}{"promptTokenCount": upstream}}),
		})
	}
	ratioUp := float64(est) / 1.1 // ratio 1.1 -> inside the band
	if v := checkTokenDriftWithinBand([]CaptureRecord{mk(ratioUp)}); len(v) != 0 {
		t.Fatalf("in-band must pass: %v", v)
	}
	v := checkTokenDriftWithinBand([]CaptureRecord{mk(ratioUp), mk(float64(est) * 4)})
	if len(v) != 2 || !hasDetail(v, "outside target band") || v[1].RID != "aggregate" || !hasDetail(v, "only 50.0% of 2") {
		t.Fatalf("outlier + aggregate verdict expected: %v", v)
	}
	if checkTokenDriftWithinBand([]CaptureRecord{rec(t, nil)}) != nil {
		t.Fatal("no samples -> nil")
	}
}

func TestCov_TokenAccountingSanityShapes(t *testing.T) {
	mk := func(claudeBody, geminiBody interface{}) CaptureRecord {
		return rec(t, map[string]interface{}{"claude_response": leg(claudeBody), "gemini_response": leg(geminiBody)})
	}
	usage := func(in float64) interface{} {
		return map[string]interface{}{"usage": map[string]interface{}{"input_tokens": in, "cache_read_input_tokens": 100.0}}
	}
	gem := func(p interface{}) interface{} {
		return map[string]interface{}{"usageMetadata": map[string]interface{}{"promptTokenCount": p}}
	}
	if len(checkTokenAccountingSanity(mk(usage(900), gem(1000.0)))) != 0 {
		t.Fatal("within tolerance")
	}
	if len(checkTokenAccountingSanity(mk(usage(5000), gem(1000.0)))) != 1 {
		t.Fatal("divergence")
	}
	for _, r := range []CaptureRecord{
		mk(map[string]interface{}{}, gem(1.0)),
		mk(usage(1), map[string]interface{}{}),
		mk(usage(1), gem(0.0)),
		mk(usage(1), gem("x")),
		rec(t, nil),
	} {
		if len(checkTokenAccountingSanity(r)) != 0 {
			t.Fatal("incomplete data must be skipped")
		}
	}
}

func TestCov_SanitizeIdempotenceGuards(t *testing.T) {
	good := rec(t, map[string]interface{}{"claude_request": leg(claudeReqBody())})
	for name, r := range map[string]CaptureRecord{
		"clean":      good,
		"no request": rec(t, nil),
		"bad shape":  rec(t, map[string]interface{}{"claude_request": leg("str")}),
		"no msgs":    rec(t, map[string]interface{}{"claude_request": leg(map[string]interface{}{"model": "m"})}),
		"unmarshal":  rec(t, map[string]interface{}{"claude_request": leg(map[string]interface{}{"f": func() {}})}),
	} {
		if v := checkSanitizeIdempotenceOnCapturedTraffic(r); len(v) != 0 {
			t.Errorf("%s: %v", name, v)
		}
	}
	if v := SanitizeIdempotenceOnCapturedTraffic.Check([]CaptureRecord{good, good}); len(v) != 0 {
		t.Fatal(v)
	}
}

func TestCov_TranslateStructuralParity(t *testing.T) {
	reqRaw, _ := json.Marshal(claudeReqBody())
	tenant := config.TenantConfig{DefaultModel: "gemini-3.8-flash-tiered", ThinkingBudget: -1, StripSearchTools: true}
	res, err := translate.ConvertClaudeRequestToGemini(reqRaw, tenant, "", "main", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	var gem map[string]interface{}
	if err := json.Unmarshal(res.GeminiBody, &gem); err != nil {
		t.Fatal(err)
	}
	mk := func(g interface{}) CaptureRecord {
		return rec(t, map[string]interface{}{"claude_request": leg(claudeReqBody()), "gemini_request": leg(g)})
	}
	if v := checkTranslateStructuralParity(mk(gem)); len(v) != 0 {
		t.Fatalf("identical translation must have no structural diff: %v", v)
	}
	if v := TranslateStructuralParity.Check([]CaptureRecord{mk(gem)}); len(v) != 0 {
		t.Fatal(v)
	}
	// A captured request missing a field the current translator emits is a structural diff.
	stripped := map[string]interface{}{}
	for k, val := range gem {
		if k != "contents" {
			stripped[k] = val
		}
	}
	if v := checkTranslateStructuralParity(mk(stripped)); len(v) == 0 || !hasDetail(v, "structural diff") {
		t.Fatalf("expected structural diff: %v", v)
	}
	// Missing legs are skipped; a translate failure on the captured input is reported.
	if len(checkTranslateStructuralParity(rec(t, nil))) != 0 {
		t.Fatal("no legs")
	}
	if len(checkTranslateStructuralParity(rec(t, map[string]interface{}{"claude_request": leg(claudeReqBody())}))) != 0 {
		t.Fatal("no gemini leg")
	}
	if len(checkTranslateStructuralParity(rec(t, map[string]interface{}{"claude_request": leg(map[string]interface{}{"f": func() {}}), "gemini_request": leg(gem)}))) != 0 {
		t.Fatal("unmarshalable claude body skipped")
	}
	bad := rec(t, map[string]interface{}{"claude_request": leg("not an object"), "gemini_request": leg(gem), "agent_key": "sub:x"})
	bad["agent_key"] = "sub:x"
	if v := checkTranslateStructuralParity(bad); len(v) != 1 || !hasDetail(v, "ConvertClaudeRequestToGemini failed") {
		t.Fatalf("translate error must be reported: %v", v)
	}
}
