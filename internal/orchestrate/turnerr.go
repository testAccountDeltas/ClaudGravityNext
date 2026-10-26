// Part of the orchestrate package — see pipeline.go for the package doc
// comment. Helpers for stream.UpstreamTurnError, the typed "this turn is
// not a usable answer" failure (abnormal finishReason, empty turn,
// truncated stream, in-band upstream error).
package orchestrate

import (
	"errors"
	"net/http"

	"claudgravity/internal/anthropicerr"
	"claudgravity/internal/stream"
)

func asTurnError(err error) *stream.UpstreamTurnError {
	var te *stream.UpstreamTurnError
	if errors.As(err, &te) {
		return te
	}
	return nil
}

// isModelTurnFailure: the model/stream produced a bad turn (as opposed to
// Google rejecting the account with an in-band 429/503) — retried on a
// different account but never a reason to put an account on cooldown.
func isModelTurnFailure(err error) bool {
	te := asTurnError(err)
	return te != nil && !te.InBand
}

// terminalSSEError picks the Anthropic error type/message for a request
// that failed after the SSE response was already committed.
func terminalSSEError(status int, err error) (string, string) {
	// Whatever the upstream said stays in the log; the client gets a short
	// message (a bare Google JSON/RPC error used to be sent as it was).
	if te := asTurnError(err); te != nil {
		return te.ErrType, anthropicerr.ClientMessage(te.Status, te.Error())
	}
	if status == 0 {
		status = http.StatusBadGateway
	}
	msg := "upstream request failed"
	if err != nil {
		msg = "Upstream failed after retries: " + anthropicerr.ClientMessage(status, err.Error())
	}
	return anthropicerr.TypeForStatus(status), msg
}
