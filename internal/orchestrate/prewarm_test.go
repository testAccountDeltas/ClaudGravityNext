package orchestrate

import "testing"

func TestApplyPrewarmShape(t *testing.T) {
	resp := func() map[string]interface{} {
		return map[string]interface{}{
			"content":     []map[string]interface{}{{"type": "text", "text": "x"}},
			"stop_reason": "end_turn",
			"usage":       map[string]interface{}{"input_tokens": 8, "output_tokens": 1, "cache_creation_input_tokens": 5120},
		}
	}
	r := resp()
	applyPrewarmShape(r, []byte(`{"max_tokens":0}`))
	if c, _ := r["content"].([]map[string]interface{}); len(c) != 0 || r["stop_reason"] != "max_tokens" {
		t.Fatalf("not rewritten: %v", r)
	}
	if u := r["usage"].(map[string]interface{}); u["output_tokens"] != 0 || u["cache_creation_input_tokens"] != 5120 {
		t.Fatalf("usage: %v", u)
	}
	for _, body := range []string{`{"max_tokens":1}`, `{"max_tokens":4096}`, `{}`, `nope`} {
		r = resp()
		applyPrewarmShape(r, []byte(body))
		if r["stop_reason"] != "end_turn" {
			t.Errorf("%s: an ordinary request must be left alone", body)
		}
	}
}
