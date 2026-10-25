package stream

import (
	"errors"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func covErrChunk(code int, status, msg string) []byte {
	s := `{"error":{`
	parts := []string{}
	if code != 0 {
		parts = append(parts, `"code":`+itoaCov(code))
	}
	if status != "" {
		parts = append(parts, `"status":"`+status+`"`)
	}
	if msg != "" {
		parts = append(parts, `"message":"`+msg+`"`)
	}
	return []byte(s + strings.Join(parts, ",") + `}}`)
}

func itoaCov(n int) string {
	if n == 0 {
		return "0"
	}
	d := ""
	for n > 0 {
		d = string(rune('0'+n%10)) + d
		n /= 10
	}
	return d
}

func TestCovInBandErrorHeldIsTypedError(t *testing.T) {
	cases := []struct {
		code       int
		status     string
		wantType   string
		wantStatus int
	}{
		{0, "RESOURCE_EXHAUSTED", "rate_limit_error", 429},
		{0, "UNAVAILABLE", "overloaded_error", 503},
		{0, "INVALID_ARGUMENT", "invalid_request_error", 400},
		{0, "FAILED_PRECONDITION", "invalid_request_error", 400},
		{0, "OUT_OF_RANGE", "invalid_request_error", 400},
		{0, "UNAUTHENTICATED", "authentication_error", 503},
		{0, "PERMISSION_DENIED", "permission_error", 503},
		{0, "NOT_FOUND", "not_found_error", 503},
		{4, "DEADLINE_EXCEEDED", "overloaded_error", 503},
		{529, "", "overloaded_error", 529},
		{400, "", "invalid_request_error", 400},
	}
	for _, c := range cases {
		f, rec, _ := heldFSM(basicParams())
		err := f.ProcessGeminiChunk(covErrChunk(c.code, c.status, "boom"))
		var terr *UpstreamTurnError
		if !errors.As(err, &terr) {
			t.Fatalf("%v/%s: want UpstreamTurnError, got %T %v", c.code, c.status, err, err)
		}
		if terr.ErrType != c.wantType || terr.Status != c.wantStatus || !terr.InBand {
			t.Errorf("%d/%s: got %+v want type=%s status=%d", c.code, c.status, terr, c.wantType, c.wantStatus)
		}
		if !strings.Contains(terr.Msg, "boom") {
			t.Errorf("message lost: %q", terr.Msg)
		}
		if rec.Body.Len() != 0 || !f.HasFinished() {
			t.Errorf("held error must write nothing and mark finished; body=%q", rec.Body.String())
		}
	}
}

func TestCovInBandErrorAfterCommitEmitsSSEError(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	if err := f.ProcessGeminiChunk(chunk(`{"text":"partial answer"}`, "")); err != nil {
		t.Fatal(err)
	}
	err := f.ProcessGeminiChunk(covErrChunk(429, "RESOURCE_EXHAUSTED", "quota"))
	if err == nil || !strings.Contains(err.Error(), "RESOURCE_EXHAUSTED") || !strings.Contains(err.Error(), "quota") {
		t.Fatalf("error should carry the google status word and message, got %v", err)
	}
	names := eventNames(rec.Body.String())
	if len(names) == 0 || names[len(names)-1] != "error" {
		t.Fatalf("last event must be error, got %v", names)
	}
	if names[len(names)-2] != "content_block_stop" {
		t.Fatalf("open block must be closed before the error: %v", names)
	}
	data := parseEventPayload(rec.Body.String(), "error")
	if gjson.Get(data, "error.type").String() != "rate_limit_error" ||
		gjson.Get(data, "error.message").String() != "Upstream stream error: quota" {
		t.Fatalf("bad error payload %s", data)
	}
	if !f.HasFinished() {
		t.Fatal("must be finished")
	}
}

func TestCovInBandErrorWithoutMessageOrStatusUsesRawAndType(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	_ = f.ProcessGeminiChunk(chunk(`{"text":"x"}`, ""))
	err := f.ProcessGeminiChunk([]byte(`{"error":{"code":503}}`))
	if err == nil || !strings.Contains(err.Error(), "(overloaded_error)") || !strings.Contains(err.Error(), `{"code":503}`) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(rec.Body.String(), "event: error") {
		t.Fatalf("no error event: %s", rec.Body.String())
	}
}

func TestCovFailTruncated(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	err := f.FailTruncated()
	var terr *UpstreamTurnError
	if !errors.As(err, &terr) || terr.ErrType != "overloaded_error" || terr.Status != 503 {
		t.Fatalf("got %v", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("held truncation writes nothing: %q", rec.Body.String())
	}
	if again := f.FailTruncated(); again != nil {
		t.Fatalf("second call must be a no-op, got %v", again)
	}

	f2, rec2, _ := heldFSM(basicParams())
	if err := f2.ProcessGeminiChunk(chunk(`{"text":"half"}`, "")); err != nil {
		t.Fatal(err)
	}
	if err := f2.FailTruncated(); !errors.As(err, &terr) {
		t.Fatalf("got %v", err)
	}
	names := eventNames(rec2.Body.String())
	if names[len(names)-1] != "error" || names[len(names)-2] != "content_block_stop" {
		t.Fatalf("committed truncation must end with a closed block and one error event: %v", names)
	}
	data := parseEventPayload(rec2.Body.String(), "error")
	if gjson.Get(data, "error.type").String() != "overloaded_error" {
		t.Fatalf("bad payload %s", data)
	}
	// no message_stop after the error
	for _, n := range names {
		if n == "message_stop" {
			t.Fatalf("message_stop must not follow a failed turn: %v", names)
		}
	}
}

func TestCovFinishWithErrorClassifiesInvalidRequest(t *testing.T) {
	f, rec, _ := heldFSM(basicParams())
	_ = f.ProcessGeminiChunk(chunk(`{"text":"x"}`, ""))
	f.FinishWithError(errors.New("rpc: PERMISSION_DENIED for project"))
	data := parseEventPayload(rec.Body.String(), "error")
	if gjson.Get(data, "error.type").String() != "invalid_request_error" {
		t.Fatalf("got %s", data)
	}
	f.FinishWithError(errors.New("again")) // idempotent
	if strings.Count(rec.Body.String(), "event: error") != 1 {
		t.Fatalf("second FinishWithError must not emit: %s", rec.Body.String())
	}
}
