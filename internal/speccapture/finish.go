// Part of the speccapture package's port from superrelay (v1)'s
// speccapture.go — see redact.go for the split rationale. This file
// holds AppendClaudeEvent, Finish (assembling one finished request's
// NDJSON line), and the day-file writer/pruner it persists through.
package speccapture

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"claudgravity/internal/logx"
)

// AppendClaudeEvent records one outbound Anthropic SSE event. Called
// from the lowest-level emit point every event type (message_start,
// content_block_delta, message_stop, ...) already passes through once
// internal/stream exists, so this is one hook for the whole event
// vocabulary.
func (s *SpecCaptureStore) AppendClaudeEvent(rid uint64, event string, data string) {
	if !s.enabled {
		return
	}
	c := s.get(rid)
	if c == nil {
		return
	}
	rec := map[string]interface{}{"event": event}
	if parsed := redactJSONBytes([]byte(data)); parsed != nil {
		rec["data"] = parsed
	}
	c.claudeEvents = append(c.claudeEvents, rec)
}

// Finish assembles whatever legs were recorded into one NDJSON line and
// drops the in-memory entry. Safe to call unconditionally on every exit
// path of a request (including ones that never called Begin, or that
// began but errored out before any other leg was recorded) — it's a no-op
// whenever there's nothing to flush.
func (s *SpecCaptureStore) Finish(rid uint64) {
	// Raw-chunk rotation runs once per finished request regardless of
	// whether the redacted capture below is enabled — the two are
	// independent switches (see InitSpecCaptureRawChunks).
	s.pruneRawChunks()

	if !s.enabled {
		return
	}
	s.mu.Lock()
	c, ok := s.inFlight[rid]
	if ok {
		delete(s.inFlight, rid)
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	out := map[string]interface{}{
		"rid":       c.rid,
		"timestamp": c.timestamp.UTC().Format(time.RFC3339Nano),
	}
	if c.sessionID != "" {
		out["session_id"] = c.sessionID
	}
	if c.agentKey != "" {
		out["agent_key"] = c.agentKey
	}
	if c.claudeRequest != nil {
		out["claude_request"] = c.claudeRequest
	}
	if c.geminiRequest != nil {
		out["gemini_request"] = c.geminiRequest
	}
	if c.geminiResponse != nil {
		out["gemini_response"] = c.geminiResponse
	} else if len(c.geminiChunks) > 0 {
		out["gemini_response"] = map[string]interface{}{"chunks": c.geminiChunks}
	}
	if c.claudeResponse != nil {
		out["claude_response"] = c.claudeResponse
	} else if len(c.claudeEvents) > 0 {
		out["claude_response"] = map[string]interface{}{"events": c.claudeEvents}
	}

	// Nothing but rid/timestamp/session/agent-key got recorded (e.g. an
	// auth failure before Begin's caller ever reached a capture point) —
	// not worth a line.
	_, hasClaudeReq := out["claude_request"]
	_, hasGeminiReq := out["gemini_request"]
	_, hasGeminiResp := out["gemini_response"]
	_, hasClaudeResp := out["claude_response"]
	if !hasClaudeReq && !hasGeminiReq && !hasGeminiResp && !hasClaudeResp {
		return
	}

	line, err := json.Marshal(out)
	if err != nil {
		return
	}
	s.writeLine(line)
}

func (s *SpecCaptureStore) writeLine(line []byte) {
	s.fileMu.Lock()
	defer s.fileMu.Unlock()
	today := time.Now().UTC().Format("2006-01-02")
	if s.file == nil || s.fileDate != today {
		if s.file != nil {
			_ = s.file.Close()
		}
		path := filepath.Join(s.dir, today+".ndjson")
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			logx.LogGateway("[SuperRelay Gateway] Warning: spec capture could not open %s: %v", path, err)
			return
		}
		s.file = f
		s.fileDate = today
		s.pruneOld()
	}
	line = append(line, '\n')
	_, _ = s.file.Write(line)
}

// pruneOld deletes capture files older than the configured retention,
// going by the date encoded in the filename rather than mtime — simpler,
// and correct even if the files were copied/touched by something else.
func (s *SpecCaptureStore) pruneOld() {
	if s.retain <= 0 {
		return
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -s.retain)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		dateStr, isCapture := strings.CutSuffix(name, ".ndjson")
		if !isCapture {
			continue
		}
		t, err := time.Parse("2006-01-02", dateStr)
		if err != nil {
			continue
		}
		if t.Before(cutoff) {
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}
