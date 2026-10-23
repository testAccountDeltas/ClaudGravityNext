// Part of the httpapi package — see errors.go for the package doc
// comment. The 1-token rate-limit-probe fast-path intercept from v1's
// handleMessages section "2.2" — Claude Code's own CLI sends this tiny
// single-text-block "quota" request to read rate-limit headers off the
// response without burning a real generation.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
	"claudgravity/internal/ratelimit"

	"github.com/tidwall/gjson"
)

// isQuotaProbeRequest reports whether bodyBytes is Claude Code's 1-token
// quota probe: a single message whose content is either the bare string
// "quota" or the single-block-array equivalent — the SDK can serialize a
// single-text-block message's content as either shape, and gjson's
// .String() on the array form returns its raw JSON (never matching
// "quota" directly), so both shapes need checking or this probe always
// misses and falls through to a full Gemini round-trip.
func isQuotaProbeRequest(bodyBytes []byte) bool {
	msgsArr := gjson.GetBytes(bodyBytes, "messages").Array()
	if len(msgsArr) != 1 {
		return false
	}
	content0 := gjson.GetBytes(bodyBytes, "messages.0.content")
	if content0.String() == "quota" {
	} else if content0.IsArray() {
		blocks := content0.Array()
		if len(blocks) != 1 || blocks[0].Get("type").String() != "text" || blocks[0].Get("text").String() != "quota" {
			return false
		}
	} else {
		return false
	}
	return gjson.GetBytes(bodyBytes, "max_tokens").Int() == 1
}

func sendQuotaProbeSSE(w http.ResponseWriter, respModel string) {
	flusher, _ := w.(http.Flusher)
	sendEvent := func(event string, data interface{}) {
		b, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, string(b))
		if flusher != nil {
			flusher.Flush()
		}
	}

	sendEvent("message_start", map[string]interface{}{
		"type": "message_start",
		"message": map[string]interface{}{
			"id":            "msg_probe",
			"type":          "message",
			"role":          "assistant",
			"model":         respModel,
			"content":       []interface{}{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]interface{}{
				"input_tokens":                1,
				"output_tokens":               1,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     0,
			},
		},
	})

	sendEvent("content_block_start", map[string]interface{}{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]interface{}{
			"type": "text",
			"text": "",
		},
	})

	sendEvent("content_block_delta", map[string]interface{}{
		"type":  "content_block_delta",
		"index": 0,
		"delta": map[string]interface{}{
			"type": "text_delta",
			"text": "ok",
		},
	})

	sendEvent("content_block_stop", map[string]interface{}{
		"type":  "content_block_stop",
		"index": 0,
	})

	sendEvent("message_delta", map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   "max_tokens",
			"stop_sequence": nil,
			"stop_details":  nil,
		},
		"usage": map[string]interface{}{
			"output_tokens": 1,
		},
	})

	sendEvent("message_stop", map[string]interface{}{
		"type": "message_stop",
	})
}

// tryQuotaProbeIntercept answers the probe locally with a stable synthetic
// response, applying the sticky account's real rate-limit headers so the
// CLI's own quota display reflects genuine data. If stream: true is
// requested, it formats the synthetic response as a valid minimal SSE sequence.
// Returns true if it fully handled the response.
func tryQuotaProbeIntercept(clientKey string, tenant config.TenantConfig, bodyBytes []byte, w http.ResponseWriter) bool {
	if !isQuotaProbeRequest(bodyBytes) {
		return false
	}
	acc := accounts.GlobalSelector().GetOrAssignStickyAccount(clientKey, tenant)
	ratelimit.ApplyHeaders(w.Header(), acc, "gemini-3.8-flash-tiered")

	// GAP-ACC-02: this always echoed the literal "claude-opus-5-5"
	// regardless of which model the client actually probed with —
	// cosmetic, but a client inspecting the probe response's own
	// "model" field (rather than just reading the rate-limit headers)
	// saw the wrong model name.
	respModel := gjson.GetBytes(bodyBytes, "model").String()
	if respModel == "" {
		respModel = "claude-opus-5-5"
	}

	if gjson.GetBytes(bodyBytes, "stream").Bool() {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		sendQuotaProbeSSE(w, respModel)
		return true
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	body, _ := json.Marshal(map[string]interface{}{
		"id": "msg_probe", "type": "message", "role": "assistant", "model": respModel,
		"content":     []map[string]interface{}{{"type": "text", "text": "ok"}},
		"stop_reason": "max_tokens",
		"usage":       map[string]interface{}{"input_tokens": 1, "output_tokens": 1},
	})
	_, _ = w.Write(body)
	return true
}
