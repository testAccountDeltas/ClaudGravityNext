// Phase 1 characterization test for the ported (partial) warmup package —
// proves the three error classifiers and WrapAntigravityPayload behave as
// in v1.
package warmup

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

func TestIsAntigravityLocationGateError_MatchesOnlyThatMessage(t *testing.T) {
	if !IsAntigravityLocationGateError(http.StatusBadRequest, "User location is not supported for the API use.") {
		t.Fatal("exact location-gate message must match")
	}
	if !IsAntigravityLocationGateError(http.StatusBadRequest, "USER LOCATION IS NOT SUPPORTED") {
		t.Fatal("match must be case-insensitive")
	}
	if IsAntigravityLocationGateError(http.StatusTooManyRequests, "User location is not supported") {
		t.Fatal("must require status 400, not just the message text")
	}
	if IsAntigravityLocationGateError(http.StatusBadRequest, "some other 400 error") {
		t.Fatal("must not match unrelated 400 errors")
	}
}

func TestIsTransientNetworkError_MatchesNetworkMarkersOnly(t *testing.T) {
	transient := []string{
		"dial tcp 1.2.3.4:443: connect: connection refused",
		"lookup daily-cloudcode-pa.sandbox.googleapis.com: no such host",
		"read: connection reset by peer",
		"context deadline exceeded",
	}
	for _, msg := range transient {
		if !IsTransientNetworkError(http.StatusBadGateway, msg) {
			t.Errorf("expected %q to be classified as a transient network error", msg)
		}
	}
	if IsTransientNetworkError(http.StatusTooManyRequests, "dial tcp: connection refused") {
		t.Fatal("must require status 502 — this is a verdict on the client's own Do() failure, not a real upstream status")
	}
	if IsTransientNetworkError(http.StatusBadGateway, "upstream returned 502 Bad Gateway") {
		t.Fatal("a REAL 502 from the upstream (no network-error marker) must not be classified as transient-network")
	}
}

func TestIsSessionTokenWallError_MatchesTokenCeilingMessages(t *testing.T) {
	if !IsSessionTokenWallError(http.StatusBadRequest, "request exceeds the maximum number of tokens allowed") {
		t.Fatal("expected the token-ceiling phrase to match")
	}
	if !IsSessionTokenWallError(http.StatusBadRequest, "limit of 1048576 tokens exceeded") {
		t.Fatal("expected the literal 1048576 ceiling number to match")
	}
	if IsSessionTokenWallError(http.StatusTooManyRequests, "exceeds the maximum number of tokens") {
		t.Fatal("must require status 400, not a 429 quota error")
	}
}

func TestWrapAntigravityPayload_StructureAndDefaults(t *testing.T) {
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`)
	wrapped := WrapAntigravityPayload("gemini-2.5-flash", payload, "", 0, "")

	var v map[string]interface{}
	if err := json.Unmarshal(wrapped, &v); err != nil {
		t.Fatalf("wrapped payload is not valid JSON: %v", err)
	}
	if v["project"] != "aicode-consumers" {
		t.Fatalf("empty projectID must default to aicode-consumers, got %v", v["project"])
	}
	if v["model"] != "gemini-2.5-flash" {
		t.Fatalf("model must be preserved, got %v", v["model"])
	}
	if v["userAgent"] != "antigravity" {
		t.Fatalf("fixed envelope fields changed from v1: %+v", v)
	}
	// requestType must be absent, not "agent" — see payload.go's comment:
	// the real client omits it, and "agent" was found to trigger a false
	// 429 RESOURCE_EXHAUSTED bucket on Google's side.
	if _, exists := v["requestType"]; exists {
		t.Fatalf("requestType must be omitted, got %v", v["requestType"])
	}
	reqID, _ := v["requestId"].(string)
	if len(reqID) < len("agent-") || reqID[:6] != "agent-" {
		t.Fatalf("requestId must be prefixed with agent-, got %q", reqID)
	}
	sessionID := gjson.GetBytes(wrapped, "request.sessionId").String()
	if sessionID == "" {
		t.Fatal("request.sessionId must be set")
	}
}

func TestWrapAntigravityPayload_PreservesCustomProjectID(t *testing.T) {
	payload := []byte(`{"contents":[]}`)
	wrapped := WrapAntigravityPayload("gemini-2.5-flash", payload, "my-real-project", 0, "")
	if got := gjson.GetBytes(wrapped, "project").String(); got != "my-real-project" {
		t.Fatalf("a non-empty projectID must be preserved, got %q", got)
	}
}

func TestWrapAntigravityPayload_SessionIDStableAcrossCalls(t *testing.T) {
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"same text"}]}]}`)
	id1 := gjson.GetBytes(WrapAntigravityPayload("m", payload, "", 0, ""), "request.sessionId").String()
	id2 := gjson.GetBytes(WrapAntigravityPayload("m", payload, "", 0, ""), "request.sessionId").String()
	if id1 != id2 {
		t.Fatalf("same payload/generation must produce the same sessionId (prefix-cache stability), got %q then %q", id1, id2)
	}
}
