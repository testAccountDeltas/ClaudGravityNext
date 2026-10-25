package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const readTools = `"tools":[{"name":"Read","input_schema":{"type":"object","properties":{"file_path":{"type":"string"},"limit":{"type":"integer"}}}}]`

func refusedRead(input, result string) string {
	return `{"model":"claude-opus-4-5","max_tokens":64,"stream":false,` + readTools + `,"messages":[` +
		`{"role":"user","content":"read it"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"Read","input":` + input + `}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","is_error":true,"content":` + result + `}]}]}`
}

const tokenRefusal = `"File content (34972 tokens) exceeds maximum allowed tokens (25000). Use offset and limit"`
const sizeRefusal = `"File content (371.4KB) exceeds maximum allowed size (256KB). Use offset and limit"`

func TestReadRetryCall_HalvesTheWindow(t *testing.T) {
	for name, c := range map[string]struct {
		input, result string
		wantLimit     int64
	}{
		"had a limit":         {`{"file_path":"/f.md","offset":10,"limit":800}`, tokenRefusal, 400},
		"allow_large dropped": {`{"file_path":"/f.md","limit":200,"allow_large":true}`, tokenRefusal, 100},
	} {
		_, in, ok := readRetryCall([]byte(refusedRead(c.input, c.result)))
		if !ok || gjson.Get(in, "limit").Int() != c.wantLimit || gjson.Get(in, "file_path").String() != "/f.md" {
			t.Errorf("%s: ok=%v input=%s", name, ok, in)
			continue
		}
		if gjson.Get(in, "allow_large").Exists() {
			t.Errorf("%s: allow_large must be dropped: %s", name, in)
		}
		if name == "had a limit" && gjson.Get(in, "offset").Int() != 10 {
			t.Errorf("offset must be kept: %s", in)
		}
	}
}

func TestReadRetryCall_LeavesEverythingElseToTheModel(t *testing.T) {
	ok := func(js string) bool { _, _, ok := readRetryCall([]byte(js)); return ok }
	if ok(refusedRead(`{"file_path":"/f.md"}`, sizeRefusal)) || ok(refusedRead(`{"file_path":"/f.md"}`, tokenRefusal)) {
		t.Error("a Read without a limit is the model's call: outline, tail or window")
	}
	if ok(refusedRead(`{"file_path":"/f","limit":100}`, tokenRefusal)) {
		t.Error("the window is already small: let the model decide")
	}
	if ok(refusedRead(`{"file_path":"/f"}`, `"no such file"`)) {
		t.Error("another error")
	}
	if ok(strings.Replace(refusedRead(`{"file_path":"/f"}`, tokenRefusal), `"name":"Read"`, `"name":"Grep"`, 1)) {
		t.Error("another tool")
	}
	if ok(strings.Replace(refusedRead(`{"file_path":"/f"}`, tokenRefusal), readTools, `"tools":[]`, 1)) {
		t.Error("the client no longer declares Read")
	}
	two := strings.Replace(refusedRead(`{"file_path":"/f"}`, tokenRefusal), `}]}]}`, `},{"type":"text","text":"also"}]}]}`, 1)
	if ok(two) {
		t.Error("a message with more than the refusal is not answered synthetically")
	}
	if ok(`{"messages":[]}`) || ok(`{}`) {
		t.Error("nothing to heal")
	}
}

func TestHandleMessages_RefusedReadIsRetriedWithoutTheModel(t *testing.T) {
	cm := newTestConfigManager(t, "sk-good")
	for _, stream := range []bool{false, true} {
		body := refusedRead(`{"file_path":"/f.md","limit":800}`, tokenRefusal)
		if stream {
			body = strings.Replace(body, `"stream":false`, `"stream":true`, 1)
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
		r.Header.Set("x-api-key", "sk-good")
		handleMessages(cm, w, r)
		out := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(out, "tool_use") || !strings.Contains(out, `/f.md`) {
			t.Fatalf("stream=%v: %d %s", stream, w.Code, out)
		}
		if stream {
			if !strings.Contains(out, "input_json_delta") || !strings.Contains(out, `\"limit\":400`) && !strings.Contains(out, `"limit":400`) {
				t.Fatalf("stream payload: %s", out)
			}
		} else if gjson.Get(out, "stop_reason").String() != "tool_use" || gjson.Get(out, "content.0.input.limit").Int() != 400 ||
			!strings.HasPrefix(gjson.Get(out, "content.0.id").String(), "toolu_") {
			t.Fatalf("response: %s", out)
		}
	}
}
