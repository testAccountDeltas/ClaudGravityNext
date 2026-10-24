// Part of the stream package — see events.go for the package doc
// comment. StreamGeminiToClaude reads the upstream Gemini SSE stream,
// buffers chunks across boundary splits, fires keepalive pings, and translates
// events to Claude SSE events via FSM.
package stream

import (
	"io"
	"strings"
	"time"
)

// keepaliveInterval is the SSE ping period while upstream is quiet. A
// package var only so tests can shrink it; production never writes it.
var keepaliveInterval = 5 * time.Second

// StreamGeminiToClaude consumes an upstream SSE stream from Gemini and
// translates them to Claude SSE events via f. onActivity, when non-nil,
// is called every time bytes are actually read from upstream — the
// caller uses this to run an idle-timeout watchdog that only cancels on
// genuine silence, not total elapsed duration (a single buffered
// function-call chunk for a huge Write can legitimately take minutes to
// arrive).
func StreamGeminiToClaude(f *FSM, r io.Reader, onActivity func()) error {
	pingTicker := time.NewTicker(keepaliveInterval)
	defer pingTicker.Stop()

	stopPing := make(chan struct{})
	defer close(stopPing)

	go func() {
		for {
			select {
			case <-pingTicker.C:
				_ = f.writer.SendKeepalivePing()
			case <-stopPing:
				return
			}
		}
	}()

	buf := make([]byte, 64*1024)
	var leftOver string

	for {
		n, err := r.Read(buf)
		if n > 0 {
			if onActivity != nil {
				onActivity()
			}
			data := leftOver + string(buf[:n])
			lines := strings.Split(data, "\n")
			leftOver = lines[len(lines)-1]

			for i := 0; i < len(lines)-1; i++ {
				line := strings.TrimSpace(lines[i])
				if strings.HasPrefix(line, "data:") {
					payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
					if payload == "[DONE]" {
						fr := "STOP"
						if f.pendingFinishReason != "" {
							fr = f.pendingFinishReason
							f.pendingFinishReason = ""
						}
						return f.FinishStream(fr)
					}
					if strings.HasPrefix(payload, "{") {
						if err := f.ProcessGeminiChunk([]byte(payload)); err != nil {
							return err
						}
						if f.HasFinished() {
							return nil
						}
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
	}

	if strings.TrimSpace(leftOver) != "" {
		line := strings.TrimSpace(leftOver)
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if strings.HasPrefix(payload, "{") {
				if err := f.ProcessGeminiChunk([]byte(payload)); err != nil {
					return err
				}
			}
		}
	}

	// EOF with no finishReason ever seen is a truncated stream (dropped
	// connection, upstream cut), never a clean end_turn.
	if f.pendingFinishReason == "" {
		return f.FailTruncated()
	}
	fr := f.pendingFinishReason
	f.pendingFinishReason = ""
	return f.FinishStream(fr)
}
