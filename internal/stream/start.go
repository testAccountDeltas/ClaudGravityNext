// Part of the stream package — see events.go for the package doc
// comment. Extracted from fsm.go to keep fsm.go under the 400-line ceiling:
// handles message_start event construction and initial token split estimation.
package stream

import (
	"encoding/json"

	"claudgravity/internal/cacheusage"
)

// EnsureMessageStart sends message_start exactly once, estimating the
// cache split from this session's own last turn (see caches.go) since
// Gemini's real split only arrives in the stream's last chunk, and
// official Anthropic SDKs latch input_tokens/cache_read_input_tokens from
// message_start only, never revising them from message_delta's real
// numbers later.
func (f *FSM) EnsureMessageStart() error {
	if f.hasStarted {
		return nil
	}
	f.hasStarted = true

	cachedEstimate := int64(0)
	uncachedEstimate := int64(f.estInputTokens)
	if int64(f.estInputTokens) >= cacheCreationThreshold(f.model) && f.hasPriorTurns {
		if frac, ok := getSessionCacheFraction(f.sessionID); ok && frac > 0 {
			cachedEstimate = int64(float64(f.estInputTokens) * frac)
			uncachedEstimate = int64(f.estInputTokens) - cachedEstimate
			if uncachedEstimate < 0 {
				uncachedEstimate = 0
			}
		}
	}
	// A same-turn cache WRITE moves a large chunk of tokens from
	// input_tokens into cache_creation_input_tokens — pre-simulate that
	// split here too (using the pre-stream estimate), or a client that
	// only reads message_start keeps its full original estimate and then
	// ADDS message_delta's cache_creation on top, double-counting a
	// turn-1 cache write. A cache-read turn and a cache-write turn are
	// NOT mutually exclusive (a breakpoint from a prior turn can be read
	// while a new breakpoint further into this turn's own fresh content
	// is also written), so this doesn't gate on cachedEstimate==0.
	cc5m, cc1h := cacheusage.Split(f.model, uncachedEstimate, cachedEstimate, f.cachePrefixFrac, f.cacheFrac1h)
	cacheCreationEstimate := cc5m + cc1h
	uncachedEstimate -= cacheCreationEstimate

	startPayload := map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            f.messageID,
			"type":          "message",
			"role":          "assistant",
			"content":       []interface{}{},
			"model":         f.model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]interface{}{
				"input_tokens":                uncachedEstimate,
				"output_tokens":               1,
				"cache_creation_input_tokens": cacheCreationEstimate,
				"cache_creation":              cacheusage.Object(cc5m, cc1h),
				"cache_read_input_tokens":     cachedEstimate,
			},
		},
	}
	b, _ := json.Marshal(startPayload)
	return f.writer.EmitEvent("message_start", string(b))
}
