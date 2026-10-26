// Part of the orchestrate package — see streaming.go. Small pieces split out of
// runStreamingAttempt to keep that file under the 400-line ceiling.
package orchestrate

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"claudgravity/internal/stream"
)

// How long a failover-capable attempt waits in silence before it starts
// pinging the client, and how often it pings afterwards. Variables so tests
// can shorten them.
var (
	failoverPingAfter = 90 * time.Second
	failoverPingEvery = 30 * time.Second
)

// reportedStreamStatus is the status recorded (and returned to the pipeline)
// for a stream that ended with err after content had already reached the
// client.
func reportedStreamStatus(baseCtx context.Context, err error, turnErr *stream.UpstreamTurnError) int {
	errStr := err.Error()
	switch {
	case turnErr != nil:
		// A bad turn that already had content on the wire (reported to
		// the client as an SSE error event by the FSM).
		return turnErr.Status
	case clientGone(baseCtx):
		return 499
	case errors.Is(err, context.Canceled) || strings.Contains(errStr, "context canceled"):
		// Idle watchdog cancelled its own derived ctx, not the client.
		return http.StatusGatewayTimeout
	case strings.Contains(errStr, "RESOURCE_EXHAUSTED") || strings.Contains(errStr, "rate_limit_error"):
		return http.StatusTooManyRequests
	case strings.Contains(errStr, "UNAVAILABLE") || strings.Contains(errStr, "overloaded_error"):
		return http.StatusServiceUnavailable
	case strings.Contains(errStr, "DEADLINE_EXCEEDED"):
		// GAP-P0-04: a bare HTTP 500/api_error here reads as fatal to
		// Claude Code's own retry logic — a real Anthropic SDK backs
		// off and retries 504/overloaded_error automatically, and a
		// Google-side timeout on a single upstream call is exactly
		// the kind of transient condition that deserves a retry, not
		// a dropped session.
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}
