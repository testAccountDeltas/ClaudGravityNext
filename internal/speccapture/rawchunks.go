// Part of the speccapture package's port from superrelay (v1)'s
// speccapture.go — see redact.go for the split rationale. This file
// holds the unredacted raw-chunk replay-corpus capture.
package speccapture

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// AppendRawGeminiChunk records one streamed Gemini chunk UNREDACTED (real
// generative text included) into request_captures/raw_chunks/<rid>.ndjson —
// the corpus cmd/replay (Part 7.5 of the v2 spec) feeds through
// internal/stream.FSM offline, without hitting Gemini again. Gated by its
// own rawEnabled switch so it can be on/off independently of the redacted
// capture. Opens/appends/closes the file per call rather than keeping a
// handle open per in-flight request — simpler and leak-free; this capture
// is meant to run for a bounded corpus-building window, not indefinitely,
// so the extra open/close cost is an acceptable trade.
func (s *SpecCaptureStore) AppendRawGeminiChunk(rid uint64, chunk []byte) {
	s.rawMu.Lock()
	enabled, dir := s.rawEnabled, s.rawDir
	s.rawMu.Unlock()
	if !enabled || len(chunk) == 0 {
		return
	}
	path := filepath.Join(dir, fmt.Sprintf("%d.ndjson", rid))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	line := append(append([]byte{}, chunk...), '\n')
	_, _ = f.Write(line)
	_ = f.Close()
}

// pruneRawChunks enforces the session-count/total-size ceiling on
// raw_chunks/ by deleting the OLDEST files (by mtime) until both limits are
// satisfied. Called once per finished request (not per chunk) from Finish —
// a directory listing capped at a couple hundred entries is cheap enough to
// do there without needing a separate background sweep goroutine.
func (s *SpecCaptureStore) pruneRawChunks() {
	s.rawMu.Lock()
	enabled, dir, maxSessions, maxBytes := s.rawEnabled, s.rawDir, s.rawMaxSessions, s.rawMaxBytes
	s.rawMu.Unlock()
	if !enabled {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type fileInfo struct {
		name    string
		size    int64
		modTime time.Time
	}
	files := make([]fileInfo, 0, len(entries))
	var totalBytes int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileInfo{name: e.Name(), size: info.Size(), modTime: info.ModTime()})
		totalBytes += info.Size()
	}
	if len(files) <= maxSessions && (maxBytes <= 0 || totalBytes <= maxBytes) {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime.Before(files[j].modTime) })
	for _, f := range files {
		if len(files) <= maxSessions && (maxBytes <= 0 || totalBytes <= maxBytes) {
			break
		}
		if err := os.Remove(filepath.Join(dir, f.name)); err == nil {
			totalBytes -= f.size
			files = files[1:]
		} else {
			break
		}
	}
}
