// Part of the stream package — see events.go for the package doc
// comment. Extracted from chunk.go (GAP-CGN-D5-01) to keep chunk.go under
// the 400-line ceiling: handles in-band upstream error decoding and
// SSE error event dispatching.
package stream

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"claudgravity/internal/anthropicerr"
)

// handleInBandError handles an in-band error payload returned within an
// otherwise-HTTP-200 SSE stream from Google Gemini.
func (f *FSM) handleInBandError(errNode gjson.Result) error {
	msg := errNode.Get("message").String()
	if msg == "" {
		msg = errNode.Raw
	}
	// Google's in-band error code mirrors HTTP status semantics (429,
	// 503, ...) even though the outer HTTP response was 200 OK. The
	// google.rpc.Status "status" string (RESOURCE_EXHAUSTED,
	// UNAVAILABLE, ...) is unambiguous regardless of whether "code" on
	// this envelope is the HTTP-style number the fallback assumes or a
	// raw gRPC status code instead — checked first as a safe supplement.
	var errType string
	switch errNode.Get("status").String() {
	case "RESOURCE_EXHAUSTED":
		errType = "rate_limit_error"
	case "UNAVAILABLE":
		errType = "overloaded_error"
	case "INVALID_ARGUMENT", "FAILED_PRECONDITION", "OUT_OF_RANGE":
		errType = "invalid_request_error"
	case "UNAUTHENTICATED":
		errType = "authentication_error"
	case "PERMISSION_DENIED":
		errType = "permission_error"
	case "NOT_FOUND":
		errType = "not_found_error"
	case "DEADLINE_EXCEEDED":
		// GAP-P0-04: an in-band timeout is exactly as transient as the
		// out-of-band one streaming.go now maps to overloaded_error/504 —
		// anthropicerr.TypeForStatus's own fallback below has no
		// DEADLINE_EXCEEDED-specific case (it only recognizes HTTP-range
		// status codes, and this envelope's "code" is gRPC's, where
		// DEADLINE_EXCEEDED is 4), so it would otherwise resolve to the
		// generic, non-retried api_error.
		errType = "overloaded_error"
	default:
		errType = anthropicerr.TypeForStatus(int(errNode.Get("code").Int()))
	}
	// GAP-P0-06: close whatever content block is still open BEFORE
	// emitting the error event and marking the stream finished — same
	// ordering FinishWithError already uses for a transport-level
	// failure (fsm.go). Without this, a client mid-"thinking"/"text"
	// block sees an error event with no preceding content_block_stop,
	// violating the SSE state machine every strict client enforces; and
	// setting hasFinished first (the old order) also silently defeated
	// FinishWithError's own close-on-finish path if it ran afterward for
	// the same stream, since FinishWithError no-ops once hasFinished is
	// already true.
	if !f.writer.IsReleased() {
		// Nothing of this turn has reached the client yet (events are still
		// held): surface the failure as a typed error so the orchestrator
		// can retry it on another account instead of committing a 200
		// stream that ends in an error frame.
		f.mu.Lock()
		f.hasFinished = true
		f.mu.Unlock()
		status := int(errNode.Get("code").Int())
		if status < 400 || status > 599 {
			status = statusForErrType(errType)
			switch errType {
			case "rate_limit_error":
				status = 429
			case "invalid_request_error":
				status = 400
			}
		}
		return &UpstreamTurnError{ErrType: errType, Status: status, InBand: true,
			Msg: "upstream stream error: " + msg}
	}
	_ = f.closeCurrentBlock()
	// GAP-D1-10: msg comes from upstream and can contain arbitrary bytes
	// — %q escapes non-ASCII/control chars as \xHH, not valid JSON per
	// RFC 8259. errType is always one of this function's own fixed
	// identifier strings, safe as %q.
	msgBytes, _ := json.Marshal("Upstream stream error: " + msg)
	errPayload := fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%s}}`, errType, msgBytes)
	_ = f.writer.EmitEvent("error", errPayload)
	// GAP-CGN-D1-01: the error event is terminal; finish the writer so
	// concurrent keepalive pings cannot fire after this error frame and
	// corrupt official Anthropic SDK stream parsers.
	f.writer.Finish()
	f.mu.Lock()
	f.hasFinished = true
	f.mu.Unlock()
	// Keep Google's status word in the text: the orchestrator maps it to the
	// reported HTTP status (429/503/504) and decides on an account cooldown
	// from it, but the free-form message alone ("Resource has been exhausted")
	// never contains it.
	statusWord := errNode.Get("status").String()
	if statusWord == "" {
		statusWord = errType
	}
	return fmt.Errorf("upstream stream error (%s): %s", statusWord, msg)
}

// FinishWithError is the fallback for when the upstream read loop dies
// mid-stream on something other than a clean EOF (network drop, a Read
// error) — without this, the error just propagates up to be logged, with
// no event:error/message_stop ever sent on a connection that had already
// committed message_start and possibly several content blocks, leaving
// the client hanging on a message_stop that never arrives.
func (f *FSM) FinishWithError(err error) {
	f.mu.Lock()
	finished := f.hasFinished
	f.hasFinished = true
	f.mu.Unlock()
	if finished {
		return
	}
	// GAP-S8: close open content block before terminal error event.
	_ = f.closeCurrentBlock()
	f.flushPendingThinkingSignatureFallback()
	// GAP-R17-02: Default transport errors to overloaded_error for client retry.
	errType := "overloaded_error"
	errStr := strings.ToUpper(err.Error())
	if strings.Contains(errStr, "INVALID_ARGUMENT") || strings.Contains(errStr, "UNAUTHENTICATED") || strings.Contains(errStr, "PERMISSION_DENIED") {
		errType = "invalid_request_error"
	}
	msgBytes, _ := json.Marshal("Stream connection broken: " + err.Error())
	errPayload := fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%s}}`, errType, msgBytes)
	_ = f.writer.EmitEvent("error", errPayload)
	// GAP-D1-09: Finish() ensures no background pings are sent after error.
	f.writer.Finish()
}

// failTurn ends a turn that must not be reported as a success. Called with
// hasFinished already set. If the client has already received events (a
// block was committed), the only honest option is an SSE error event; if
// everything is still held, nothing is written and the caller retries.
func (f *FSM) failTurn(terr *UpstreamTurnError) error {
	if f.writer.IsReleased() {
		_ = f.closeCurrentBlock()
		f.flushPendingThinkingSignatureFallback()
		msgBytes, _ := json.Marshal("Upstream turn failed: " + terr.Error())
		errPayload := fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%s}}`, terr.ErrType, msgBytes)
		_ = f.writer.EmitEvent("error", errPayload)
		f.writer.Finish()
	}
	return terr
}

// FailTruncated reports a stream that ended without any finishReason.
func (f *FSM) FailTruncated() error {
	f.mu.Lock()
	if f.hasFinished {
		f.mu.Unlock()
		return nil
	}
	f.hasFinished = true
	f.mu.Unlock()
	return f.failTurn(TruncatedTurnError())
}

// WriteSSEError writes one terminal SSE error event straight to a response
// whose headers are already committed (used by the orchestrator when every
// retry failed after an earlier attempt had to commit keepalive pings).
func WriteSSEError(w http.ResponseWriter, flusher http.Flusher, errType, message string) {
	msgBytes, _ := json.Marshal(message)
	payload := fmt.Sprintf(`{"type":"error","error":{"type":%q,"message":%s}}`, errType, msgBytes)
	_ = writeAndFlush(w, flusher, formatSSEEvent("error", payload))
}
