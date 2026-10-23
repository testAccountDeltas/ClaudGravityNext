// Part of the speccapture package's port from superrelay (v1)'s
// speccapture.go — see redact.go for the split rationale. This file
// holds the store type, its two Init functions, and the per-leg capture
// methods (Begin/SetSessionMeta/Record*/AppendGeminiChunk).
package speccapture

import (
	"net/http"
	"os"
	"sync"
	"time"

	"claudgravity/internal/logx"
)

// SpecSnapshot is one captured leg (a request or a response) of the
// four-leg round trip this gateway sits in the middle of.
type SpecSnapshot struct {
	Headers map[string]string `json:"headers,omitempty"`
	Body    interface{}       `json:"body,omitempty"`
}

// inFlightCapture accumulates the four legs of one request while it's
// still being processed. The streaming legs (geminiChunks/claudeEvents)
// are appended to incrementally from internal/stream (once that exists),
// since a streamed response never exists as one complete body the way a
// non-streaming one does.
type inFlightCapture struct {
	rid       uint64
	timestamp time.Time
	sessionID string
	agentKey  string

	claudeRequest  *SpecSnapshot
	geminiRequest  *SpecSnapshot
	geminiResponse *SpecSnapshot
	claudeResponse *SpecSnapshot

	geminiChunks []interface{}
	claudeEvents []interface{}
}

// SpecCaptureStore is the package-level, mutex-guarded store every capture
// call goes through, so no function signature across the orchestration
// path needs a new parameter threaded through just to reach this — every
// call site already has the request's `rid` in scope.
type SpecCaptureStore struct {
	mu       sync.Mutex
	enabled  bool
	dir      string
	retain   int
	inFlight map[uint64]*inFlightCapture

	fileMu   sync.Mutex
	fileDate string
	file     *os.File

	// Raw (unredacted) chunk capture — see rawchunks.go. Separate on/off
	// switch and separate storage from the redacted capture above: this
	// corpus exists only to feed the offline StreamFSM replay tests (Part
	// 7.5 of the v2 spec) and is meant to be enabled briefly, not left on
	// indefinitely.
	rawMu          sync.Mutex
	rawEnabled     bool
	rawDir         string
	rawMaxSessions int
	rawMaxBytes    int64
}

var globalSpecCapture = &SpecCaptureStore{inFlight: make(map[uint64]*inFlightCapture)}

// Global exposes the package-level SpecCaptureStore singleton to
// internal/orchestrate (which records the Gemini request/response and
// Claude SSE events the same way v1's server.go/sse.go called
// globalSpecCapture directly) — same accessor convention as
// loopguard.Global()/accounts.Manager().
func Global() *SpecCaptureStore { return globalSpecCapture }

// InitSpecCapture wires up the store from config at startup. Safe to call
// with enabled=false — every method below becomes a near-zero-cost no-op.
func InitSpecCapture(enabled bool, dir string, retentionDays int) {
	if dir == "" {
		dir = "request_captures"
	}
	globalSpecCapture.mu.Lock()
	globalSpecCapture.enabled = enabled
	globalSpecCapture.dir = dir
	globalSpecCapture.retain = retentionDays
	globalSpecCapture.mu.Unlock()
	if !enabled {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		logx.LogGateway("[SuperRelay Gateway] Warning: spec capture disabled, could not create %s: %v", dir, err)
		globalSpecCapture.mu.Lock()
		globalSpecCapture.enabled = false
		globalSpecCapture.mu.Unlock()
		return
	}
	globalSpecCapture.pruneOld()
}

// InitSpecCaptureRawChunks wires up the raw (unredacted) chunk corpus
// independently of InitSpecCapture — it can be on even if the redacted
// capture above is off, and vice versa. rawDir defaults to "<dir>/raw_chunks".
func InitSpecCaptureRawChunks(enabled bool, dir string, maxSessions int, maxBytes int64) {
	globalSpecCapture.rawMu.Lock()
	globalSpecCapture.rawEnabled = enabled
	globalSpecCapture.rawDir = dir
	globalSpecCapture.rawMaxSessions = maxSessions
	globalSpecCapture.rawMaxBytes = maxBytes
	globalSpecCapture.rawMu.Unlock()
	if !enabled {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		logx.LogGateway("[SuperRelay Gateway] Warning: raw chunk capture disabled, could not create %s: %v", dir, err)
		globalSpecCapture.rawMu.Lock()
		globalSpecCapture.rawEnabled = false
		globalSpecCapture.rawMu.Unlock()
	}
}

func (s *SpecCaptureStore) Begin(rid uint64) {
	if !s.enabled {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inFlight[rid] = &inFlightCapture{rid: rid, timestamp: time.Now()}
}

func (s *SpecCaptureStore) get(rid uint64) *inFlightCapture {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight[rid]
}

// SetSessionMeta attaches the gateway's own session/agent identifiers —
// the same ones the dashboard already shows per request — to the capture
// so `cmd/speccheck -session <id>` can find every request belonging to
// one conversation. Called once sessionID is known (shortly before
// translation, later than Begin), so it's a separate method rather than
// a Begin parameter.
func (s *SpecCaptureStore) SetSessionMeta(rid uint64, sessionID, agentKey string) {
	if !s.enabled {
		return
	}
	if c := s.get(rid); c != nil {
		c.sessionID = sessionID
		c.agentKey = agentKey
	}
}

func (s *SpecCaptureStore) RecordClaudeRequest(rid uint64, headers http.Header, body []byte) {
	if !s.enabled {
		return
	}
	if c := s.get(rid); c != nil {
		c.claudeRequest = &SpecSnapshot{Headers: redactHeaders(headers), Body: redactJSONBytes(body)}
	}
}

func (s *SpecCaptureStore) RecordGeminiRequest(rid uint64, headers http.Header, body []byte) {
	if !s.enabled {
		return
	}
	if c := s.get(rid); c != nil {
		c.geminiRequest = &SpecSnapshot{Headers: redactHeaders(headers), Body: redactJSONBytes(body)}
	}
}

func (s *SpecCaptureStore) RecordGeminiResponse(rid uint64, headers http.Header, body []byte) {
	if !s.enabled {
		return
	}
	if c := s.get(rid); c != nil {
		c.geminiResponse = &SpecSnapshot{Headers: redactHeaders(headers), Body: redactJSONBytes(body)}
	}
}

func (s *SpecCaptureStore) RecordClaudeResponse(rid uint64, headers http.Header, body interface{}) {
	if !s.enabled {
		return
	}
	if c := s.get(rid); c != nil {
		var redacted interface{}
		if raw, ok := body.([]byte); ok {
			redacted = redactJSONBytes(raw)
		} else {
			redacted = redactSkeleton(body)
		}
		c.claudeResponse = &SpecSnapshot{Headers: redactHeaders(headers), Body: redacted}
	}
}

// AppendGeminiChunk records one streamed Gemini SSE/JSON chunk. Called
// from internal/stream's chunk processing (once that exists) — one hook
// covers every chunk shape Gemini can send.
func (s *SpecCaptureStore) AppendGeminiChunk(rid uint64, chunk []byte) {
	if !s.enabled {
		return
	}
	if c := s.get(rid); c != nil {
		c.geminiChunks = append(c.geminiChunks, redactJSONBytes(chunk))
	}
}
