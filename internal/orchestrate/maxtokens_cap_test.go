package orchestrate

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Regression tests for the client max_tokens contract: visible-output cap,
// stop_reason/usage consistency, tool_use exemption, and the one retry of a
// MAX_TOKENS turn whose hidden thinking left no visible output.

const capReq = `{"model":"claude-sonnet-4-5","max_tokens":%d,"messages":[{"role":"user","content":"hi"}]%s}`

var capLongText = strings.Repeat("alpha beta gamma delta ", 60)

// capChunk builds one upstream chunk. usage is the raw usageMetadata body.
func capChunk(parts, finish, usage string) string {
	fr := ""
	if finish != "" {
		fr = `,"finishReason":"` + finish + `"`
	}
	return `{"response":{"candidates":[{"content":{"role":"model","parts":[` + parts + `]}` + fr + `}],"usageMetadata":{` + usage + `}}}`
}

const capNoCandUsage = `"promptTokenCount":100`

func TestCap_Streaming_ClipsVisibleTextAndReportsMaxTokens(t *testing.T) {
	r := tfRun(t, fmt.Sprintf(capReq, 20, ""), true, func(n int, _ string) (int, string) {
		half := len(capLongText) / 2
		return 200, tfSSE(
			capChunk(`{"text":`+q(capLongText[:half])+`}`, "", capNoCandUsage),
			capChunk(`{"text":`+q(capLongText[half:])+`}`, "STOP", capNoCandUsage))
	})
	if !r.ok || r.attempts != 1 {
		t.Fatalf("ok=%v attempts=%d err=%v body=%s", r.ok, r.attempts, r.err, r.body)
	}
	if !strings.Contains(r.body, `"stop_reason":"max_tokens"`) {
		t.Fatalf("want stop_reason max_tokens: %s", r.body)
	}
	var got string
	for _, l := range strings.Split(r.body, "\n") {
		if strings.Contains(l, `"text_delta"`) {
			got += gjson.Get(strings.TrimPrefix(l, "data: "), "delta.text").String()
		}
	}
	if got == "" || len(got) >= len(capLongText)/4 {
		t.Fatalf("visible text must be clipped to ~20 tokens, got %d bytes", len(got))
	}
	if out := gjson.Get(lastDataLine(r.body, "message_delta"), "usage.output_tokens").Int(); out != 20 {
		t.Fatalf("output_tokens=%d, want exactly max_tokens (20): %s", out, r.body)
	}
	if strings.Count(r.body, "event: message_stop") != 1 {
		t.Fatalf("exactly one message_stop expected: %s", r.body)
	}
}

func TestCap_Streaming_ExactUpstreamCountWithinCapIsNeverClipped(t *testing.T) {
	text := "short answer that fits"
	r := tfRun(t, fmt.Sprintf(capReq, 20, ""), true, func(n int, _ string) (int, string) {
		return 200, tfSSE(capChunk(`{"text":`+q(text)+`}`, "STOP", `"promptTokenCount":100,"candidatesTokenCount":5`))
	})
	if !strings.Contains(r.body, text) || strings.Contains(r.body, `"max_tokens"`) {
		t.Fatalf("answer within cap must pass untouched: %s", r.body)
	}
}

func TestCap_Streaming_ToolUseIsExempt(t *testing.T) {
	args := `{"file_path":"/a/very/long/path/` + strings.Repeat("segment/", 40) + `x.go"}`
	r := tfRun(t, fmt.Sprintf(capReq, 5, `,"tools":[{"name":"Read","description":"d","input_schema":{"type":"object","properties":{"file_path":{"type":"string"}}}}]`), true,
		func(n int, _ string) (int, string) {
			return 200, tfSSE(capChunk(`{"functionCall":{"name":"Read","args":`+args+`}}`, "STOP", capNoCandUsage))
		})
	if !strings.Contains(r.body, `"stop_reason":"tool_use"`) || !strings.Contains(r.body, "segment") {
		t.Fatalf("tool_use must be delivered whole with stop_reason tool_use: %s", r.body)
	}
}

func TestCap_NonStreaming_ClipsTextAndReportsMaxTokens(t *testing.T) {
	r := tfRun(t, fmt.Sprintf(capReq, 20, ""), false, func(n int, _ string) (int, string) {
		return 200, capChunk(`{"text":`+q(capLongText)+`}`, "STOP", `"promptTokenCount":100,"thoughtsTokenCount":7`)
	})
	if !r.ok || r.attempts != 1 {
		t.Fatalf("ok=%v attempts=%d err=%v body=%s", r.ok, r.attempts, r.err, r.body)
	}
	if gjson.Get(r.body, "stop_reason").String() != "max_tokens" {
		t.Fatalf("want max_tokens: %s", r.body)
	}
	if txt := gjson.Get(r.body, "content.0.text").String(); txt == "" || len(txt) >= len(capLongText)/4 {
		t.Fatalf("text must be trimmed to ~20 tokens, got %d bytes", len(txt))
	}
	if out := gjson.Get(r.body, "usage.output_tokens").Int(); out != 20+7 {
		t.Fatalf("output_tokens=%d, want max_tokens + thinking (27)", out)
	}
}

func TestCap_NonStreaming_StopSequenceCountScenarioNotMaxTokens(t *testing.T) {
	// "Count from 1 to 10, stop at 5": Gemini strips the stop sequence, the
	// answer is short and finishReason is STOP.
	r := tfRun(t, fmt.Sprintf(capReq, 200, `,"stop_sequences":["5"]`), false, func(n int, _ string) (int, string) {
		return 200, capChunk(`{"text":"1, 2, 3, 4"}`, "STOP", `"promptTokenCount":100,"candidatesTokenCount":8,"thoughtsTokenCount":218`)
	})
	if gjson.Get(r.body, "content.0.text").String() != "1, 2, 3, 4" {
		t.Fatalf("answer must be delivered whole: %s", r.body)
	}
	if sr := gjson.Get(r.body, "stop_reason").String(); sr != "end_turn" && sr != "stop_sequence" {
		t.Fatalf("stop_reason=%s must not be max_tokens: %s", sr, r.body)
	}
}

func thoughtOnlyMaxTokens() string {
	return capChunk(`{"thought":true,"text":"hmm"}`, "MAX_TOKENS", `"promptTokenCount":100,"thoughtsTokenCount":1200`)
}

func TestCap_EmptyMaxTokens_RetriesOnceWithWiderAllowance(t *testing.T) {
	for _, streaming := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			var firstMax, secondMax int64
			r := tfRun(t, fmt.Sprintf(capReq, 200, ""), streaming, func(n int, body string) (int, string) {
				m := gjson.Get(body, "request.generationConfig.maxOutputTokens").Int()
				good := capChunk(`{"text":"visible answer"}`, "STOP", `"promptTokenCount":100,"candidatesTokenCount":3`)
				bad := thoughtOnlyMaxTokens()
				if streaming {
					good, bad = tfSSE(good), tfSSE(bad)
				}
				if n == 1 {
					firstMax = m
					return 200, bad
				}
				secondMax = m
				return 200, good
			})
			if !r.ok || r.attempts != 2 || !strings.Contains(r.body, "visible answer") {
				t.Fatalf("ok=%v attempts=%d err=%v body=%s", r.ok, r.attempts, r.err, r.body)
			}
			if r.auths[0] != r.auths[1] {
				t.Fatalf("the widening retry must stay on the same account")
			}
			if secondMax <= firstMax || firstMax == 0 {
				t.Fatalf("maxOutputTokens must grow on the retry: first=%d second=%d", firstMax, secondMax)
			}
		})
	}
}

func TestCap_EmptyMaxTokens_SecondTimeIsReportedAsMaxTokens(t *testing.T) {
	for _, streaming := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%v", streaming), func(t *testing.T) {
			r := tfRun(t, fmt.Sprintf(capReq, 200, ""), streaming, func(n int, _ string) (int, string) {
				if streaming {
					return 200, tfSSE(thoughtOnlyMaxTokens())
				}
				return 200, thoughtOnlyMaxTokens()
			})
			if !r.ok || r.attempts != 2 {
				t.Fatalf("exactly one widening retry expected: ok=%v attempts=%d err=%v body=%s", r.ok, r.attempts, r.err, r.body)
			}
			if !strings.Contains(r.body, `"stop_reason":"max_tokens"`) && !strings.Contains(r.body, `"stop_reason": "max_tokens"`) {
				t.Fatalf("want a final max_tokens message: %s", r.body)
			}
		})
	}
}

func q(s string) string { return fmt.Sprintf("%q", s) }

func lastDataLine(body, event string) string {
	lines := strings.Split(body, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] == "event: "+event && i+1 < len(lines) {
			return strings.TrimPrefix(lines[i+1], "data: ")
		}
	}
	return ""
}
