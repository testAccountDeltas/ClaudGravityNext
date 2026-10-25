package speccapture

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func covReadRecords(t *testing.T, dir string) []map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".ndjson"))
	if err != nil {
		t.Fatalf("capture file: %v", err)
	}
	var out []map[string]interface{}
	for _, ln := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(ln), &m); err != nil {
			t.Fatalf("bad line %q: %v", ln, err)
		}
		out = append(out, m)
	}
	return out
}

func TestCov_StreamedLegsAssembledIntoChunksAndEvents(t *testing.T) {
	s := newTestCaptureStore(t)
	if Global() != s {
		t.Fatal("Global must return the singleton")
	}
	const rid = uint64(7)
	s.Begin(rid)
	s.AppendGeminiChunk(rid, []byte(`{"candidates":[{"i":1}]}`))
	s.AppendGeminiChunk(rid, []byte(`not json`))
	s.AppendClaudeEvent(rid, "message_start", `{"type":"message_start"}`)
	s.AppendClaudeEvent(rid, "ping", `garbage{`)
	s.AppendClaudeEvent(rid, "empty", ``)
	s.RecordClaudeRequest(rid, http.Header{"Authorization": {"Bearer abcdefghijkl"}}, []byte(`{"m":1}`))
	s.Finish(rid)

	recs := covReadRecords(t, s.dir)
	if len(recs) != 1 {
		t.Fatalf("records: %d", len(recs))
	}
	gr := recs[0]["gemini_response"].(map[string]interface{})["chunks"].([]interface{})
	if len(gr) != 2 {
		t.Fatalf("chunks: %v", gr)
	}
	if gr[1].(map[string]interface{})["_unparsed_len"].(float64) != 8 {
		t.Fatalf("unparsable chunk must record only its length: %v", gr[1])
	}
	ev := recs[0]["claude_response"].(map[string]interface{})["events"].([]interface{})
	if len(ev) != 3 || ev[0].(map[string]interface{})["event"] != "message_start" {
		t.Fatalf("events: %v", ev)
	}
	if _, has := ev[2].(map[string]interface{})["data"]; has {
		t.Fatal("empty data must be omitted")
	}
	hdrs := recs[0]["claude_request"].(map[string]interface{})["headers"].(map[string]interface{})
	if strings.Contains(strings.ToLower(jsonStr(hdrs)), "abcdefghijkl") {
		t.Fatalf("credential leaked into capture: %v", hdrs)
	}
}

func jsonStr(v interface{}) string { b, _ := json.Marshal(v); return string(b) }

func TestCov_ClaudeResponseNonBytesBodyIsRedactedSkeleton(t *testing.T) {
	s := newTestCaptureStore(t)
	const rid = uint64(8)
	s.Begin(rid)
	s.SetSessionMeta(rid, "", "")
	big := strings.Repeat("x", specRedactThreshold+50)
	s.RecordClaudeResponse(rid, nil, map[string]interface{}{"text": big, "n": 1})
	s.Finish(rid)
	recs := covReadRecords(t, s.dir)
	body := recs[0]["claude_response"].(map[string]interface{})["body"].(map[string]interface{})
	if !strings.HasPrefix(body["text"].(string), "<redacted text:") {
		t.Fatalf("long text must be redacted: %v", body["text"])
	}
	if _, ok := recs[0]["session_id"]; ok {
		t.Fatal("empty session id must be omitted")
	}
}

func TestCov_DisabledStoreIsNoOp(t *testing.T) {
	dir := t.TempDir()
	InitSpecCapture(false, dir, 0)
	s := Global()
	s.Begin(1)
	s.SetSessionMeta(1, "a", "b")
	s.RecordClaudeRequest(1, nil, []byte(`{}`))
	s.RecordGeminiRequest(1, nil, []byte(`{}`))
	s.RecordGeminiResponse(1, nil, []byte(`{}`))
	s.RecordClaudeResponse(1, nil, []byte(`{}`))
	s.AppendGeminiChunk(1, []byte(`{}`))
	s.AppendClaudeEvent(1, "e", `{}`)
	s.Finish(1)
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("disabled store wrote files: %v", entries)
	}
	// Enabled, but never-begun rid: all legs are silently ignored.
	s2 := newTestCaptureStore(t)
	s2.RecordClaudeRequest(99, nil, []byte(`{}`))
	s2.AppendGeminiChunk(99, []byte(`{}`))
	s2.AppendClaudeEvent(99, "e", `{}`)
	s2.RecordGeminiRequest(99, nil, []byte(`{}`))
	s2.RecordGeminiResponse(99, nil, []byte(`{}`))
	s2.RecordClaudeResponse(99, nil, []byte(`{}`))
	s2.SetSessionMeta(99, "a", "b")
	s2.Finish(99)
	if _, err := os.Stat(filepath.Join(s2.dir, time.Now().UTC().Format("2006-01-02")+".ndjson")); err == nil {
		t.Fatal("nothing should have been written")
	}
}

func TestCov_InitFailuresDisableCapture(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	InitSpecCapture(true, filepath.Join(blocker, "sub"), 1)
	if globalSpecCapture.enabled {
		t.Fatal("unusable dir must disable capture")
	}
	InitSpecCaptureRawChunks(true, filepath.Join(blocker, "raw"), 1, 1)
	if globalSpecCapture.rawEnabled {
		t.Fatal("unusable raw dir must disable raw capture")
	}
	InitSpecCaptureRawChunks(false, "", 0, 0)
	// Empty dir defaults to request_captures (disabled so nothing is created).
	InitSpecCapture(false, "", 0)
	if globalSpecCapture.dir != "request_captures" {
		t.Fatalf("default dir: %q", globalSpecCapture.dir)
	}
}

func TestCov_RedactJSONBytes(t *testing.T) {
	if redactJSONBytes(nil) != nil {
		t.Fatal("empty -> nil")
	}
	m, ok := redactJSONBytes([]byte(`{{`)).(map[string]interface{})
	if !ok || m["_unparsed_len"] != 2 {
		t.Fatalf("unparsed: %v", m)
	}
	short := redactJSONBytes([]byte(`{"a":"hi"}`)).(map[string]interface{})
	if short["a"] != "hi" {
		t.Fatal("short strings kept")
	}
}
