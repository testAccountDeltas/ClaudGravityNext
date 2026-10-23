// Part of the warmup package's partial port from superrelay (v1)'s
// warmup.go — see errors.go for the split rationale and what's
// deliberately not yet ported.
package warmup

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"github.com/google/uuid"

	"claudgravity/internal/sessionkey"
)

// AntigravityHosts is the host list used by the one-off warmup probe
// (ported alongside WrapAntigravityPayload since both travel together in
// v1; the probe itself — WarmupSingleAccount — is deferred, see errors.go).
var AntigravityHosts = []string{
	"https://daily-cloudcode-pa.sandbox.googleapis.com",
	"https://daily-cloudcode-pa.googleapis.com",
}

// MainInferenceHosts is the fallback order for the main inference path
// specifically — NOT the same list as AntigravityHosts (used only by the
// one-off warmup probe, where hitting production once is low-stakes).
// Sandbox first: the plain daily host throttles far more readily (measured
// independently in clirelay: the daily host rate-limited requests the
// sandbox host then served without complaint). Production is deliberately
// excluded: it answers every request from a free-tier account with
// `429 RESOURCE_EXHAUSTED`, and a 429 here trips account cooldown,
// poisoning every subsequent request on this account for 15 minutes — a
// host that only ever says "quota" can never serve the caller that just
// got location-gated in the first place.
var MainInferenceHosts = []string{
	"https://daily-cloudcode-pa.sandbox.googleapis.com",
	"https://daily-cloudcode-pa.googleapis.com",
}

// WrapAntigravityPayload wraps a Gemini request for the Antigravity envelope.
// generation selects which deterministic session bucket to use: 0 is the
// conversation's normal, stable bucket (same every call, for prompt-cache
// reuse); bumping it (1, 2, ...) rotates to a fresh session bucket on the
// SAME account/conversation, which is how a request recovers from Google's
// per-session ~1,048,576-token wall.
func WrapAntigravityPayload(modelName string, payload []byte, projectID string, generation int, fallbackSessionKey string) []byte {
	var requestRaw string
	if reqNode := gjson.GetBytes(payload, "request"); reqNode.IsObject() {
		requestRaw = reqNode.Raw
	} else {
		requestRaw = string(payload)
	}

	pid := projectID
	if pid == "" {
		pid = "aicode-consumers"
	}

	template := `{"project":"","model":"","userAgent":"antigravity","requestType":"agent","requestId":"","request":{}}`
	template, _ = sjson.Set(template, "project", pid)
	template, _ = sjson.Set(template, "model", modelName)
	template, _ = sjson.Set(template, "requestId", "agent-"+uuid.NewString())
	template, _ = sjson.SetRaw(template, "request", requestRaw)

	// Derive stable session ID for prefix cache
	sessionID := sessionkey.GenerateStableSessionID(payload, generation, fallbackSessionKey)
	template, _ = sjson.Set(template, "request.sessionId", sessionID)

	return []byte(template)
}
