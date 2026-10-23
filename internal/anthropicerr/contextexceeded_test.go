package anthropicerr

import "testing"

func TestContextExceededMapping(t *testing.T) {
	raw := `upstream failed after retries: HTTP 400: {"error":{"code":400,"message":"The input token count (1500000) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}`
	if !IsContextExceeded(400, raw) {
		t.Fatal("must match Google overflow wording")
	}
	want := "prompt is too long: 1500000 tokens > 1048576 maximum"
	if got := PromptTooLongMessage(raw, 10, 20); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if IsContextExceeded(429, raw) {
		t.Fatal("non-400 must not match")
	}
	if IsContextExceeded(400, "Invalid JSON payload received") {
		t.Fatal("unrelated 400 must not match")
	}
	if IsContextExceeded(400, "tool name too long") {
		t.Fatal("'too long' without tokens must not match")
	}
	if got := PromptTooLongMessage("request is too long, token count exceeded", 900, 800); got != "prompt is too long: 900 tokens > 800 maximum" {
		t.Fatalf("fallback to estimate: %q", got)
	}
	if got := PromptTooLongMessage("", 100, 800); got != "prompt is too long: 801 tokens > 800 maximum" {
		t.Fatalf("estimate below limit must read as overflow: %q", got)
	}
}
