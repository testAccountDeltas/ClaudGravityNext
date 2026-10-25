// Part of the httpapi package — see errors.go for the package doc comment.
// Self-healing for a Read the client refused as too large.
//
// Claude Code's Read refuses a file that is over its output cap (about 25k
// tokens, or 256 KB without a limit). The model then has to notice the error
// and re-issue Read with a window — a wasted turn that also pollutes the
// conversation. The gateway sees that refusal as the tail of the next
// request, so instead of paying a model call to retry, it answers with the
// retry itself: the same Read with a smaller window. The model is not called.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"claudgravity/internal/logx"
	"claudgravity/internal/toolcallid"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// readRetryDefaultLimit is the window for a Read that had no limit.
	readRetryDefaultLimit = 400
	// readRetryMinLimit stops the halving: below this a retry will not help and
	// the model should see the error and decide for itself.
	readRetryMinLimit = 60
)

// readRetryCall inspects bodyBytes and, when the newest user message carries
// a "too large" refusal for a Read call of the previous assistant message,
// returns that call's input rewritten with a smaller window.
func readRetryCall(bodyBytes []byte) (toolName, inputJSON string, ok bool) {
	msgs := gjson.GetBytes(bodyBytes, "messages").Array()
	if len(msgs) < 2 {
		return "", "", false
	}
	last, prev := msgs[len(msgs)-1], msgs[len(msgs)-2]
	if last.Get("role").String() != "user" || prev.Get("role").String() != "assistant" || !last.Get("content").IsArray() {
		return "", "", false
	}
	var failed []gjson.Result
	for _, b := range last.Get("content").Array() {
		if b.Get("type").String() == "tool_result" && b.Get("is_error").Bool() &&
			strings.Contains(b.Get("content").Raw, "exceeds maximum allowed") {
			failed = append(failed, b)
		}
	}
	// Only a lone refusal is healed: with several results in the message the
	// model has other things to react to, and a synthetic reply would drop them.
	if len(failed) != 1 || len(last.Get("content").Array()) != 1 {
		return "", "", false
	}
	var call gjson.Result
	for _, b := range prev.Get("content").Array() {
		if b.Get("type").String() == "tool_use" && b.Get("id").String() == failed[0].Get("tool_use_id").String() {
			call = b
		}
	}
	if !call.Exists() || !strings.EqualFold(call.Get("name").String(), "read") || call.Get("input.file_path").String() == "" {
		return "", "", false
	}
	if !declaresTool(bodyBytes, call.Get("name").String()) {
		return "", "", false
	}
	limit := call.Get("input.limit").Int()
	next := int64(readRetryDefaultLimit)
	if limit > 0 {
		next = limit / 2
	}
	if next < readRetryMinLimit {
		return "", "", false
	}
	in := call.Get("input").Raw
	in, _ = sjson.Set(in, "limit", next)
	in, _ = sjson.Delete(in, "allow_large")
	return call.Get("name").String(), in, true
}

func declaresTool(bodyBytes []byte, name string) bool {
	for _, t := range gjson.GetBytes(bodyBytes, "tools").Array() {
		if t.Get("name").String() == name {
			return true
		}
	}
	return false
}

// tryReadRetryIntercept answers a refused Read with the retry call itself.
// Returns true if it fully handled the response.
func tryReadRetryIntercept(rid uint64, bodyBytes []byte, w http.ResponseWriter) bool {
	name, input, ok := readRetryCall(bodyBytes)
	if !ok {
		return false
	}
	logx.LogGateway("[#%d] >> INTERCEPT Read refused as too large: re-issuing %s", rid, input)
	model := gjson.GetBytes(bodyBytes, "model").String()
	id := toolcallid.Next(name)
	if gjson.GetBytes(bodyBytes, "stream").Bool() {
		writeToolUseSSE(w, model, id, name, input)
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	body, _ := json.Marshal(map[string]interface{}{
		"id": "msg_readretry", "type": "message", "role": "assistant", "model": model,
		"content":     []interface{}{map[string]interface{}{"type": "tool_use", "id": id, "name": name, "input": json.RawMessage(input)}},
		"stop_reason": "tool_use", "stop_sequence": nil,
		"usage": map[string]interface{}{"input_tokens": 1, "output_tokens": 1},
	})
	_, _ = w.Write(body)
	return true
}

func writeToolUseSSE(w http.ResponseWriter, model, id, name, input string) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	send := func(event string, data interface{}) {
		b, _ := json.Marshal(data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		if flusher != nil {
			flusher.Flush()
		}
	}
	send("message_start", map[string]interface{}{"type": "message_start", "message": map[string]interface{}{
		"id": "msg_readretry", "type": "message", "role": "assistant", "model": model,
		"content": []interface{}{}, "stop_reason": nil, "stop_sequence": nil,
		"usage": map[string]interface{}{"input_tokens": 1, "output_tokens": 1,
			"cache_creation_input_tokens": 0, "cache_read_input_tokens": 0},
	}})
	send("content_block_start", map[string]interface{}{"type": "content_block_start", "index": 0,
		"content_block": map[string]interface{}{"type": "tool_use", "id": id, "name": name, "input": map[string]interface{}{}}})
	send("content_block_delta", map[string]interface{}{"type": "content_block_delta", "index": 0,
		"delta": map[string]interface{}{"type": "input_json_delta", "partial_json": input}})
	send("content_block_stop", map[string]interface{}{"type": "content_block_stop", "index": 0})
	send("message_delta", map[string]interface{}{"type": "message_delta",
		"delta": map[string]interface{}{"stop_reason": "tool_use", "stop_sequence": nil},
		"usage": map[string]interface{}{"output_tokens": 1}})
	send("message_stop", map[string]interface{}{"type": "message_stop"})
}
