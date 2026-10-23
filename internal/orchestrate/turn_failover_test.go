package orchestrate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
	"claudgravity/internal/reqctx"
	"claudgravity/internal/translate"
)

// Regression tests for the W1 stream/turn-integrity fixes, driven through
// the real Execute() retry loop against a mock Gemini upstream.

const tfReq = `{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]%s}`

type tfResult struct {
	ok       bool
	status   int
	err      error
	body     string
	attempts int
	auths    []string
}

type tfUpstream func(n int, body string) (int, string)

func tfRun(t *testing.T, claude string, streaming bool, up tfUpstream) tfResult {
	t.Helper()
	return tfRunN(t, 2, claude, streaming, up)
}

func tfRunN(t *testing.T, nAcc int, claude string, streaming bool, up tfUpstream) tfResult {
	t.Helper()
	origTransport := httpClient.Transport
	defer func() { httpClient.Transport = origTransport }()
	restore := accounts.SetHTTPClientForTest(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(req.Body)
		tok := "fresh-2"
		if strings.Contains(string(b), "ref1") {
			tok = "fresh-1"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"access_token":%q,"expires_in":3600}`, tok))), Header: make(http.Header)}, nil
	})})
	defer restore()
	dir := t.TempDir()
	for i := 0; i < nAcc; i++ {
		e := fmt.Sprintf("w1-%d@example.com", i)
		_ = os.WriteFile(filepath.Join(dir, e+".json"), []byte(fmt.Sprintf(`{"email":%q,"access_token":"token-%d","refresh_token":"ref%d"}`, e, i+1, i+1)), 0644)
	}
	if err := accounts.InitAccountManager(dir, config.ServerConfig{}); err != nil {
		t.Fatal(err)
	}
	res := tfResult{}
	httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		res.attempts++
		b, _ := io.ReadAll(req.Body)
		res.auths = append(res.auths, req.Header.Get("Authorization"))
		st, body := up(res.attempts, string(b))
		return &http.Response{StatusCode: st, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	tenant := config.TenantConfig{Name: "t", GoogleAuthType: "oauth", DefaultModel: "gemini-3.8-flash-tiered"}
	tr, err := translate.ConvertClaudeRequestToGemini([]byte(claude), tenant, "", "main", "sess-"+t.Name(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	p := &reqctx.Params{RID: 950, SessionID: "sess-" + t.Name(), ConvAnchor: "anc-" + t.Name(), Tenant: tenant,
		ServerCfg: config.ServerConfig{PanicTimeoutSeconds: 5, TTFTTimeoutSeconds: 5, ReadTimeoutSeconds: 5, MaxStreamDurationSeconds: 10},
		ModelName: "claude-sonnet-4-5", IsStreaming: streaming, IsAntigravity: true, PrefillText: tr.PrefillText,
		SyntheticReasonTools: tr.SyntheticReasonTools, ReverseToolNameMap: tr.ReverseToolNameMap, RawClaudeBody: []byte(claude)}
	st := &reqctx.State{StartedAt: time.Now(), ResolvedModelName: tr.ResolvedModel, GeminiBody: tr.GeminiBody, EstInputTokens: tr.EstimatedInputTokens}
	rec := httptest.NewRecorder()
	res.ok, res.status, res.err = (&Orchestrator{}).Execute(context.Background(), p, st, rec)
	res.body = rec.Body.String()
	return res
}

func tfWrap(cand string) string {
	return `{"response":{"candidates":[` + cand + `],"usageMetadata":{"promptTokenCount":1000,"cachedContentTokenCount":300,"candidatesTokenCount":3}}}`
}

func tfSSE(chunks ...string) string {
	s := ""
	for _, c := range chunks {
		s += "data: " + c + "\n\n"
	}
	return s
}

const tfGood = `{"content":{"role":"model","parts":[{"text":"hello from a healthy turn"}]},"finishReason":"STOP"}`

func TestW1_Streaming_BadFinishReasonThenRetryOnOtherAccount(t *testing.T) {
	for _, fr := range []string{"MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL", "TOO_MANY_TOOL_CALLS", "MISSING_THOUGHT_SIGNATURE", "OTHER", "MALFORMED_RESPONSE"} {
		t.Run(fr, func(t *testing.T) {
			bad := tfSSE(tfWrap(`{"content":{"role":"model","parts":[{"thought":true,"text":"thinking"}]},"finishReason":"` + fr + `"}`))
			r := tfRun(t, fmt.Sprintf(tfReq, ""), true, func(n int, _ string) (int, string) {
				if n == 1 {
					return 200, bad
				}
				return 200, tfSSE(tfWrap(tfGood))
			})
			if !r.ok || r.attempts != 2 {
				t.Fatalf("ok=%v attempts=%d status=%d err=%v body=%s", r.ok, r.attempts, r.status, r.err, r.body)
			}
			if r.auths[0] == r.auths[1] {
				t.Fatalf("retry must prefer a different account, both used %s", r.auths[0])
			}
			if !strings.Contains(r.body, "hello from a healthy turn") || strings.Contains(r.body, "event: error") ||
				strings.Count(r.body, "event: message_start") != 1 {
				t.Fatalf("client must see exactly one clean message: %s", r.body)
			}
			if strings.Contains(r.body, `"type":"thinking"`) {
				t.Fatalf("discarded attempt's thinking leaked to the client: %s", r.body)
			}
		})
	}
}

func TestW1_Streaming_AllAttemptsBad_NothingCommitted_RealErrorNotEndTurn(t *testing.T) {
	empty := tfSSE(tfWrap(`{"content":{"role":"model","parts":[]},"finishReason":"STOP"}`))
	r := tfRun(t, fmt.Sprintf(tfReq, ""), true, func(n int, _ string) (int, string) { return 200, empty })
	if r.ok {
		t.Fatalf("nothing was ever committed, so Execute must return ok=false and let the caller write a real HTTP error; body=%s", r.body)
	}
	if r.attempts != 3 {
		t.Fatalf("want 1 try + 2 retries = 3 attempts, got %d", r.attempts)
	}
	if r.status != http.StatusServiceUnavailable && r.status != http.StatusInternalServerError {
		t.Fatalf("want 503/500, got %d", r.status)
	}
	if r.body != "" {
		t.Fatalf("no bytes may be written (no 200 end_turn, no placeholder): %q", r.body)
	}
}

func TestW1_Streaming_TruncatedStreamRetries(t *testing.T) {
	trunc := tfSSE(tfWrap(`{"content":{"role":"model","parts":[{"thought":true,"text":"partial"}]}}`)) // no finishReason, EOF, only thinking so far
	r := tfRun(t, fmt.Sprintf(tfReq, ""), true, func(n int, _ string) (int, string) {
		if n == 1 {
			return 200, trunc
		}
		return 200, tfSSE(tfWrap(tfGood))
	})
	if !r.ok || r.attempts != 2 || strings.Contains(r.body, "partial") || !strings.Contains(r.body, "healthy turn") {
		t.Fatalf("truncation before commit must be retried transparently: ok=%v attempts=%d body=%s", r.ok, r.attempts, r.body)
	}
}

func TestW1_Streaming_InBandErrorFailsOver(t *testing.T) {
	for _, code := range []string{`{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Resource exhausted"}`, `{"code":503,"status":"UNAVAILABLE","message":"overloaded"}`} {
		r := tfRun(t, fmt.Sprintf(tfReq, ""), true, func(n int, _ string) (int, string) {
			if n == 1 {
				return 200, "data: {\"error\":" + code + "}\n\n"
			}
			return 200, tfSSE(tfWrap(tfGood))
		})
		if !r.ok || r.attempts != 2 || strings.Contains(r.body, "event: error") || !strings.Contains(r.body, "healthy turn") {
			t.Fatalf("in-band %s must fail over before headers: ok=%v attempts=%d body=%s", code, r.ok, r.attempts, r.body)
		}
	}
}

func TestW1_Streaming_FailureAfterContent_IsSSEErrorAndNotRetried(t *testing.T) {
	bad := tfSSE(
		tfWrap(`{"content":{"role":"model","parts":[{"text":"half an answer"}]}}`),
		tfWrap(`{"content":{"role":"model","parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL"}`),
	)
	r := tfRun(t, fmt.Sprintf(tfReq, ""), true, func(n int, _ string) (int, string) { return 200, bad })
	if !r.ok || r.attempts != 1 {
		t.Fatalf("content already sent: no retry, response is final. ok=%v attempts=%d", r.ok, r.attempts)
	}
	if !strings.Contains(r.body, "event: error") || strings.Contains(r.body, "message_stop") || strings.Contains(r.body, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected a terminal SSE error, not a clean end: %s", r.body)
	}
}

func TestW1_Streaming_UsageInMessageDelta(t *testing.T) {
	r := tfRun(t, fmt.Sprintf(tfReq, ""), true, func(n int, _ string) (int, string) { return 200, tfSSE(tfWrap(tfGood)) })
	var delta string
	for _, l := range strings.Split(r.body, "\n") {
		if strings.HasPrefix(l, "data: ") && gjson.Get(l[6:], "type").String() == "message_delta" {
			delta = l[6:]
		}
	}
	u := gjson.Get(delta, "usage")
	if u.Get("input_tokens").Int() != 700 || u.Get("cache_read_input_tokens").Int() != 300 || u.Get("output_tokens").Int() != 3 || !u.Get("cache_creation_input_tokens").Exists() {
		t.Fatalf("message_delta.usage must carry real input/cache/output, got %s", u.Raw)
	}
}

func TestW1_Streaming_SafetyBlockNoContent_RefusalWithoutPlaceholder(t *testing.T) {
	blk := tfSSE(tfWrap(`{"finishReason":"SAFETY"}`))
	r := tfRun(t, fmt.Sprintf(tfReq, ""), true, func(n int, _ string) (int, string) { return 200, blk })
	if !r.ok || r.attempts != 1 || !strings.Contains(r.body, `"stop_reason":"refusal"`) || strings.Contains(r.body, "not able to help") || strings.Contains(r.body, `"type":"text"`) {
		t.Fatalf("genuine safety block: one attempt, refusal, no injected text. attempts=%d body=%s", r.attempts, r.body)
	}
}

func TestW1_NonStreaming_BadTurnRetriesThenErrors(t *testing.T) {
	bad := tfWrap(`{"finishReason":"MALFORMED_FUNCTION_CALL"}`)
	r := tfRun(t, fmt.Sprintf(tfReq, ""), false, func(n int, _ string) (int, string) {
		if n == 1 {
			return 200, bad
		}
		return 200, tfWrap(tfGood)
	})
	if !r.ok || r.attempts != 2 || !strings.Contains(r.body, "healthy turn") {
		t.Fatalf("non-streaming bad turn must retry: ok=%v attempts=%d body=%s", r.ok, r.attempts, r.body)
	}

	r = tfRun(t, fmt.Sprintf(tfReq, ""), false, func(n int, _ string) (int, string) { return 200, tfWrap(`{"finishReason":"STOP"}`) })
	if r.ok || r.attempts != 3 || r.body != "" {
		t.Fatalf("empty turn x3 must end in a real error, never 200/(empty response): ok=%v attempts=%d body=%q", r.ok, r.attempts, r.body)
	}
}

func TestW1_NonStreaming_NoPlaceholders(t *testing.T) {
	r := tfRun(t, fmt.Sprintf(tfReq, ""), false, func(n int, _ string) (int, string) { return 200, tfWrap(`{"finishReason":"SAFETY"}`) })
	if !r.ok || gjson.Get(r.body, "stop_reason").String() != "refusal" || len(gjson.Get(r.body, "content").Array()) != 0 || !gjson.Get(r.body, "stop_details").Exists() {
		t.Fatalf("refusal must have empty content + stop_details: %s", r.body)
	}
	r = tfRun(t, fmt.Sprintf(tfReq, ""), false, func(n int, _ string) (int, string) { return 200, tfWrap(`{"finishReason":"CONTINUATION"}`) })
	if !r.ok || gjson.Get(r.body, "stop_reason").String() != "max_tokens" || strings.Contains(r.body, "empty response") {
		t.Fatalf("CONTINUATION must map to max_tokens without filler: %s", r.body)
	}
	r = tfRun(t, fmt.Sprintf(tfReq, ""), false, func(n int, _ string) (int, string) {
		return 200, tfWrap(`{"content":{"role":"model","parts":[{"text":"quoted"}]},"finishReason":"RECITATION"}`)
	})
	if !r.ok || gjson.Get(r.body, "stop_reason").String() != "end_turn" {
		t.Fatalf("RECITATION is not a refusal: %s", r.body)
	}
}

func TestW1_NonStreaming_StopSequenceEchoedIsCut(t *testing.T) {
	r := tfRun(t, fmt.Sprintf(tfReq, `,"stop_sequences":["###"]`), false, func(n int, _ string) (int, string) {
		return 200, tfWrap(`{"content":{"role":"model","parts":[{"text":"answer ###"}]},"finishReason":"STOP"}`)
	})
	if gjson.Get(r.body, "stop_reason").String() != "stop_sequence" || gjson.Get(r.body, "stop_sequence").String() != "###" ||
		gjson.Get(r.body, "content.0.text").String() != "answer " {
		t.Fatalf("echoed stop sequence must be detected and cut: %s", r.body)
	}
}

func TestW1_CommitTracker_WrapKeepsFlusherSemantics(t *testing.T) {
	rec := httptest.NewRecorder()
	w, c := trackCommit(rec)
	if _, ok := w.(http.Flusher); !ok {
		t.Fatal("flusher-capable writer must stay a Flusher")
	}
	if c.Committed() {
		t.Fatal("fresh tracker must not be committed")
	}
	_, _ = w.Write([]byte("x"))
	if !c.Committed() {
		t.Fatal("a write must mark committed")
	}
	w2, again := trackCommit(w)
	if w2 != w || !again.Committed() {
		t.Fatal("already-tracked writer must not be double wrapped")
	}
}

func TestW1_Streaming_TruncatedAfterTextIsSSEErrorNotCleanEnd(t *testing.T) {
	trunc := tfSSE(tfWrap(`{"content":{"role":"model","parts":[{"text":"partial answer"}]}}`))
	r := tfRun(t, fmt.Sprintf(tfReq, ""), true, func(n int, _ string) (int, string) { return 200, trunc })
	if !r.ok || r.attempts != 1 {
		t.Fatalf("text already committed: no retry. ok=%v attempts=%d", r.ok, r.attempts)
	}
	if !strings.Contains(r.body, "event: error") || !strings.Contains(r.body, "overloaded_error") || strings.Contains(r.body, "message_stop") {
		t.Fatalf("truncation after content must be an SSE overloaded_error: %s", r.body)
	}
}
