// Part of the orchestrate package — see pipeline.go for the package doc
// comment. Test seam for packages that drive the whole handler stack
// (internal/httpapi end-to-end tests) against a fake Gemini upstream.
package orchestrate

import (
	"net/http"
	"time"
)

// SetTransportForTest replaces the Google-facing round tripper and returns
// a function restoring the previous one. Not for production use.
func SetTransportForTest(rt http.RoundTripper) func() {
	orig := httpClient.Transport
	httpClient.Transport = rt
	return func() { httpClient.Transport = orig }
}

// SetWatchdogForTest shrinks the idle watchdog's check interval and the
// streaming inter-chunk timeout, returning a function restoring both. Not
// for production use.
func SetWatchdogForTest(tick, chunkTimeout time.Duration) func() {
	oldTick, oldChunk := watchdogTick, streamChunkTimeout
	watchdogTick, streamChunkTimeout = tick, chunkTimeout
	return func() { watchdogTick, streamChunkTimeout = oldTick, oldChunk }
}
