package orchestrate

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/stream"
)

// streamShape folds an SSE body into the same normalized form as shapeOf
// does for a non-streaming message: ordered blocks plus stop_reason.
func streamShape(body string) (blocks []string, stop string) {
	var cur []string
	var kinds []string
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(l, "data: ") {
			continue
		}
		d := l[6:]
		switch gjson.Get(d, "type").String() {
		case "content_block_start":
			kinds = append(kinds, gjson.Get(d, "content_block.type").String()+"|"+gjson.Get(d, "content_block.name").String())
			cur = append(cur, "")
		case "content_block_delta":
			i := len(cur) - 1
			switch gjson.Get(d, "delta.type").String() {
			case "text_delta":
				cur[i] += gjson.Get(d, "delta.text").String()
			case "thinking_delta":
				cur[i] += gjson.Get(d, "delta.thinking").String()
			case "input_json_delta":
				cur[i] += gjson.Get(d, "delta.partial_json").String()
			}
		case "message_delta":
			stop = gjson.Get(d, "delta.stop_reason").String()
		}
	}
	for i, k := range kinds {
		blocks = append(blocks, normBlock(strings.SplitN(k, "|", 2), cur[i]))
	}
	return blocks, stop
}

func normBlock(kn []string, payload string) string {
	switch kn[0] {
	case "tool_use":
		return "tool_use:" + kn[1] + ":" + gjson.Parse(payload).Raw
	default:
		return kn[0] + ":" + payload
	}
}

func shapeOf(body string) (blocks []string, stop string) {
	for _, b := range gjson.Get(body, "content").Array() {
		switch b.Get("type").String() {
		case "text":
			blocks = append(blocks, "text:"+b.Get("text").String())
		case "thinking":
			blocks = append(blocks, "thinking:"+b.Get("thinking").String())
		case "tool_use":
			blocks = append(blocks, "tool_use:"+b.Get("name").String()+":"+gjson.Parse(b.Get("input").Raw).Raw)
		}
	}
	return blocks, gjson.Get(body, "stop_reason").String()
}

func TestParity_StreamingVsNonStreaming_SameFixtures(t *testing.T) {
	prefillReq := `{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"json please"},{"role":"assistant","content":"Here is the JSON: {"}]}`
	cases := []struct {
		name, claude, cand string
	}{
		{"text then MAX_TOKENS", "", `{"content":{"role":"model","parts":[{"text":"partial answer cut"}]},"finishReason":"MAX_TOKENS"}`},
		{"plain text", "", `{"content":{"role":"model","parts":[{"text":"all done."}]},"finishReason":"STOP"}`},
		{"tool call", "", `{"content":{"role":"model","parts":[{"functionCall":{"name":"Read","args":{"file_path":"/a/b.go"}}}]},"finishReason":"STOP"}`},
		{"safety block", "", `{"finishReason":"SAFETY"}`},
		{"thinking then text", "", `{"content":{"role":"model","parts":[{"thought":true,"text":"let me think"},{"text":"the answer"}]},"finishReason":"STOP"}`},
		{"text before tool call", "", `{"content":{"role":"model","parts":[{"text":"checking"},{"functionCall":{"name":"Read","args":{"file_path":"/x"}}}]},"finishReason":"STOP"}`},
		{"prefill continuation", prefillReq, `{"content":{"role":"model","parts":[{"text":"\"a\": 1}"}]},"finishReason":"STOP"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := runExec(t, execOpts{streaming: true, claude: c.claude, up: func(int, *http.Request, string) (*http.Response, error) {
				return respBody(200, tfSSE(tfWrap(c.cand))), nil
			}})
			rn := runExec(t, execOpts{streaming: false, claude: c.claude, up: func(int, *http.Request, string) (*http.Response, error) {
				return respBody(200, tfWrap(c.cand)), nil
			}})
			if !rs.ok || !rn.ok {
				t.Fatalf("stream ok=%v (%v) non-stream ok=%v (%v)", rs.ok, rs.err, rn.ok, rn.err)
			}
			sb, ss := streamShape(rs.body)
			nb, ns := shapeOf(rn.body)
			if fmt.Sprint(sb) != fmt.Sprint(nb) || ss != ns {
				t.Fatalf("paths disagree\n stream:     %q stop=%s\n non-stream: %q stop=%s", sb, ss, nb, ns)
			}
		})
	}
}

func TestTerminalSSEError_Mapping(t *testing.T) {
	typ, msg := terminalSSEError(0, nil)
	if typ == "" || msg != "upstream request failed" {
		t.Fatalf("zero status + nil err: %q %q", typ, msg)
	}
	for status, want := range map[int]string{429: "rate_limit_error", 503: "overloaded_error", 400: "invalid_request_error", 504: "timeout_error"} {
		if got, m := terminalSSEError(status, errors.New("x")); got != want || !strings.Contains(m, "x") {
			t.Errorf("status %d -> %q (%q), want %q", status, got, m, want)
		}
	}
	te := &stream.UpstreamTurnError{ErrType: "api_error", Status: 500, Msg: "bad turn"}
	if got, m := terminalSSEError(503, te); got != "api_error" || !strings.Contains(m, "bad turn") {
		t.Fatalf("a typed turn error wins over the status: %q %q", got, m)
	}
	wrapped := fmt.Errorf("upstream failed after retries: %w", te)
	if got, _ := terminalSSEError(429, wrapped); got != "api_error" {
		t.Fatalf("wrapped turn error must still be recognised, got %q", got)
	}
}

// An error after the response was committed must not carry raw Google detail.
func TestTerminalSSEError_DoesNotLeakRawUpstreamDetail(t *testing.T) {
	raw := errors.New(`HTTP 403 from https://cloudcode-pa.googleapis.com: {"error":{"code":403,"status":"PERMISSION_DENIED"}}`)
	_, msg := terminalSSEError(403, raw)
	if strings.Contains(msg, "googleapis") || strings.ContainsAny(msg, "{}") || msg == "" {
		t.Fatalf("raw upstream detail reached the client: %q", msg)
	}
	te := &stream.UpstreamTurnError{ErrType: "api_error", Status: 500, Msg: `upstream stream error: {"error":{"message":"internal"}}`}
	if _, msg := terminalSSEError(500, te); strings.ContainsAny(msg, "{}") {
		t.Fatalf("a typed turn error leaked its JSON: %q", msg)
	}
}
