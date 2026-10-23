// Phase 1 characterization test for the ported speccapture package —
// proves the redaction threshold, header masking, the full
// Begin/Record/Finish lifecycle producing exactly one NDJSON line, and
// the independent raw-chunk capture + rotation all behave as in v1.
package speccapture

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRedactSkeleton_ThresholdBehavior(t *testing.T) {
	short := strings.Repeat("a", specRedactThreshold)
	long := strings.Repeat("a", specRedactThreshold+1)
	v := map[string]interface{}{
		"short_field": short,
		"long_field":  long,
		"role":        "user",
		"count":       float64(3),
	}
	got := redactSkeleton(v).(map[string]interface{})
	if got["short_field"] != short {
		t.Fatalf("a string at exactly the threshold must NOT be redacted, got %v", got["short_field"])
	}
	wantLong := "<redacted text:" + strconv.Itoa(len(long)) + " chars>"
	if got["long_field"] != wantLong {
		t.Fatalf("a string over the threshold must be redacted with its length, got %v want %v", got["long_field"], wantLong)
	}
	if got["role"] != "user" || got["count"] != float64(3) {
		t.Fatal("short strings, numbers, and other scalar types must pass through untouched")
	}
}

func TestIsSpecSensitiveHeader_MatchesCredentialsNotRateLimitTokens(t *testing.T) {
	sensitive := []string{"Authorization", "X-Api-Key", "Proxy-Authorization", "X-Some-Secret", "X-Access-Token", "X-Refresh-Token"}
	for _, h := range sensitive {
		if !isSpecSensitiveHeader(h) {
			t.Errorf("%q must be classified as sensitive", h)
		}
	}
	// The exact false-positive this function's doc comment warns about:
	// rate-limit NUMBER headers containing the substring "token" must NOT
	// be masked, or the capture loses exactly the structural data it
	// exists to keep.
	notSensitive := []string{"anthropic-ratelimit-tokens-limit", "anthropic-ratelimit-tokens-remaining", "anthropic-ratelimit-tokens-reset", "Content-Type"}
	for _, h := range notSensitive {
		if isSpecSensitiveHeader(h) {
			t.Errorf("%q must NOT be classified as sensitive (rate-limit token count, not a credential)", h)
		}
	}
}

func TestRedactHeaders_MasksValueKeepsPresence(t *testing.T) {
	h := http.Header{}
	h.Set("X-Api-Key", "sk-supersecretvalue1234")
	h.Set("Content-Type", "application/json")
	got := redactHeaders(h)
	if !strings.HasPrefix(got["X-Api-Key"], "<present,") {
		t.Fatalf("sensitive header value must be masked to presence+length, got %q", got["X-Api-Key"])
	}
	if got["Content-Type"] != "application/json" {
		t.Fatalf("non-sensitive header must pass through unmasked, got %q", got["Content-Type"])
	}
}

func newTestCaptureStore(t *testing.T) *SpecCaptureStore {
	t.Helper()
	dir := t.TempDir()
	// Point the package-level singleton at an isolated temp dir instead of
	// the real request_captures/ — InitSpecCapture already supports
	// re-initialization, which is exactly what each test needs for
	// isolation. The open day-file handle from whatever this test writes
	// must be closed before t.TempDir()'s own cleanup tries to remove the
	// directory, or Windows refuses the removal (open-file deletion
	// behaves differently than on Linux) — this is a test-isolation
	// artifact of reusing the package-level singleton across tests, not a
	// behavior difference from v1.
	InitSpecCapture(true, dir, 14)
	t.Cleanup(func() {
		globalSpecCapture.fileMu.Lock()
		if globalSpecCapture.file != nil {
			_ = globalSpecCapture.file.Close()
			globalSpecCapture.file = nil
		}
		globalSpecCapture.fileMu.Unlock()
	})
	return globalSpecCapture
}

func TestFullLifecycle_ProducesOneNDJSONLine(t *testing.T) {
	s := newTestCaptureStore(t)
	const rid = uint64(42)

	s.Begin(rid)
	s.SetSessionMeta(rid, "sess-abc", "main")
	s.RecordClaudeRequest(rid, http.Header{"X-Api-Key": {"secret123456789"}}, []byte(`{"model":"claude-opus-4-5"}`))
	s.RecordGeminiRequest(rid, nil, []byte(`{"contents":[]}`))
	s.RecordGeminiResponse(rid, nil, []byte(`{"candidates":[]}`))
	s.RecordClaudeResponse(rid, nil, []byte(`{"type":"message"}`))
	s.Finish(rid)

	path := filepath.Join(s.dir, time.Now().UTC().Format("2006-01-02")+".ndjson")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected a capture file to exist: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 NDJSON line for one finished request, got %d", len(lines))
	}

	var rec map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("captured line is not valid JSON: %v", err)
	}
	if rec["session_id"] != "sess-abc" {
		t.Fatalf("session_id must be captured, got %v", rec["session_id"])
	}
	for _, leg := range []string{"claude_request", "gemini_request", "gemini_response", "claude_response"} {
		if _, ok := rec[leg]; !ok {
			t.Errorf("expected leg %q to be present in the captured record", leg)
		}
	}

	// get(rid) must now return nil — Finish deletes the in-flight entry.
	if s.get(rid) != nil {
		t.Fatal("Finish must remove the in-flight capture entry")
	}
}

func TestFinish_NoOpWhenNothingRecorded(t *testing.T) {
	s := newTestCaptureStore(t)
	const rid = uint64(99)

	s.Begin(rid)
	s.Finish(rid) // nothing recorded between Begin and Finish

	path := filepath.Join(s.dir, time.Now().UTC().Format("2006-01-02")+".ndjson")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("a request with no recorded legs must not produce a capture line/file")
	}
}

func TestAppendRawGeminiChunk_WritesOnlyWhenEnabled(t *testing.T) {
	rawDir := t.TempDir()
	InitSpecCaptureRawChunks(false, rawDir, 100, 50*1024*1024)
	globalSpecCapture.AppendRawGeminiChunk(1, []byte(`{"text":"hello"}`))
	if entries, _ := os.ReadDir(rawDir); len(entries) != 0 {
		t.Fatal("AppendRawGeminiChunk must be a no-op while disabled")
	}

	InitSpecCaptureRawChunks(true, rawDir, 100, 50*1024*1024)
	globalSpecCapture.AppendRawGeminiChunk(1, []byte(`{"text":"hello"}`))
	data, err := os.ReadFile(filepath.Join(rawDir, "1.ndjson"))
	if err != nil {
		t.Fatalf("expected raw chunk file to exist once enabled: %v", err)
	}
	if strings.TrimSpace(string(data)) != `{"text":"hello"}` {
		t.Fatalf("raw chunk capture must be UNREDACTED (no length threshold applied), got %q", string(data))
	}
}

func TestPruneRawChunks_DropsOldestBySessionCount(t *testing.T) {
	rawDir := t.TempDir()
	InitSpecCaptureRawChunks(true, rawDir, 2, 0) // max 2 sessions, no byte limit
	s := globalSpecCapture

	now := time.Now()
	for i, rid := range []uint64{1, 2, 3} {
		path := filepath.Join(rawDir, strconv.FormatUint(rid, 10)+".ndjson")
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatalf("seed file %d: %v", rid, err)
		}
		mtime := now.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	s.pruneRawChunks()

	entries, _ := os.ReadDir(rawDir)
	if len(entries) != 2 {
		t.Fatalf("expected exactly 2 files to remain (max sessions=2), got %d", len(entries))
	}
	if _, err := os.Stat(filepath.Join(rawDir, "1.ndjson")); !os.IsNotExist(err) {
		t.Fatal("the OLDEST file (rid=1) must be the one pruned, not an arbitrary one")
	}
}
