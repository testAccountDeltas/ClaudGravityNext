package cacheusage

import (
	"testing"
)

func TestAnalyzeCountsAndTTL(t *testing.T) {
	body := []byte(`{"system":[{"type":"text","text":"a","cache_control":{"type":"ephemeral","ttl":"1h"}}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"\"cache_control\": {\"type\":\"ephemeral\"}","cache_control":{"type":"ephemeral"}}]}]}`)
	info := Analyze(body)
	if info.Count != 2 {
		t.Fatalf("count=%d want 2 (escaped literal must not count)", info.Count)
	}
	if info.Frac1h <= 0 || info.Frac1h >= info.PrefixFrac {
		t.Fatalf("frac1h=%v prefix=%v", info.Frac1h, info.PrefixFrac)
	}
	if Analyze([]byte(`{"cache_control":null}`)).Count != 0 {
		t.Fatal("null must not count")
	}
	if Analyze([]byte(`{"cache_control":{"type":"persistent"}}`)).Count != 0 {
		t.Fatal("non-ephemeral must not count")
	}
}

func TestMinTokens(t *testing.T) {
	cases := map[string]int64{
		"claude-opus-4-5": 4096, "claude-opus-4-1": 1024, "claude-sonnet-4-5": 1024,
		"claude-sonnet-4-6": 2048, "claude-haiku-4-5": 4096, "claude-3-5-haiku": 1024,
		"claude-haiku-3-5": 2048, "gemini-x": 1024,
	}
	for m, want := range cases {
		if got := MinTokens(m); got != want {
			t.Errorf("%s: got %d want %d", m, got, want)
		}
	}
}

func TestSplitNeverInvents(t *testing.T) {
	// below minimum -> 0
	if a, b := Split("claude-sonnet-4-5", 1234, 0, 0.5, 0); a+b != 0 {
		t.Fatalf("prefix 617 < 1024 must be 0, got %d %d", a, b)
	}
	// cache read present -> 0
	if a, b := Split("claude-sonnet-4-5", 5000, 100, 0.9, 0); a+b != 0 {
		t.Fatal("cache read present must be 0")
	}
	// no breakpoint -> 0
	if a, b := Split("claude-sonnet-4-5", 5000, 0, 0, 0); a+b != 0 {
		t.Fatal("no breakpoint must be 0")
	}
	a, b := Split("claude-sonnet-4-5", 10000, 0, 0.5, 0.2)
	if a != 3000 || b != 2000 {
		t.Fatalf("got %d %d", a, b)
	}
	if o := Object(1, 2); o["ephemeral_1h_input_tokens"] != int64(2) {
		t.Fatal(o)
	}
}
