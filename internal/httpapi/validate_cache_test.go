package httpapi

import (
	"strings"
	"testing"
)

func TestValidate_RejectsMoreThanFourCacheBreakpoints(t *testing.T) {
	block := `{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}`
	mk := func(n int) string {
		return `{"model":"m","max_tokens":1,"system":[` + strings.TrimSuffix(strings.Repeat(block+",", n), ",") +
			`],"messages":[{"role":"user","content":"hi"}]}`
	}
	if msg, ok := validateMessagesRequest([]byte(mk(4))); !ok {
		t.Fatalf("4 breakpoints must pass, got %q", msg)
	}
	msg, ok := validateMessagesRequest([]byte(mk(5)))
	if ok || !strings.Contains(msg, "maximum of 4") {
		t.Fatalf("5 breakpoints must be rejected, got ok=%v msg=%q", ok, msg)
	}
}
