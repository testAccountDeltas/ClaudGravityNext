package tiering

import "testing"

func TestNormalizeModelID(t *testing.T) {
	cases := map[string]string{
		"Claude-Opus-4-5": "claude-opus-4-5",
		"us.anthropic.claude-sonnet-4-5-20250929-v1:0":   "claude-sonnet-4-5",
		"eu.anthropic.claude-3-5-haiku-20241022-v1:0":    "claude-3-5-haiku",
		"global.anthropic.claude-opus-4-5":               "claude-opus-4-5",
		"bedrock/anthropic.claude-3-haiku-20240307-v1:0": "claude-3-haiku",
		"vertex_ai/claude-sonnet-4@20250514":             "claude-sonnet-4",
		"vertex/claude-opus-4-1@20250805":                "claude-opus-4-1",
		"claude-3-5-sonnet-latest":                       "claude-3-5-sonnet",
		"claude-opus-4-6[1m]":                            "claude-opus-4-6",
		"claude-sonnet-4-5-20250929":                     "claude-sonnet-4-5",
		"gemini-2.5-pro":                                 "gemini-2.5-pro",
		"":                                               "",
	}
	for in, want := range cases {
		if got := NormalizeModelID(in); got != want {
			t.Errorf("NormalizeModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveTieringNormalisedAliases(t *testing.T) {
	for _, id := range []string{"OPUS", "us.anthropic.claude-opus-4-5-20251101-v1:0", "claude-opus-4-1@20250805", "claude-opus-4-5[1m]", "claude-3-opus-latest"} {
		m, _ := ResolveTieringAndEffort(map[string]interface{}{"model": id}, "", "")
		if m != "gemini-3.8-flash-tiered" {
			t.Errorf("%s -> %s, want gemini-3.8-flash-tiered", id, m)
		}
	}
	m, _ := ResolveTieringAndEffort(map[string]interface{}{"model": "eu.anthropic.claude-3-5-haiku-20241022-v1:0"}, "", "")
	if m != "gemini-2.5-flash" {
		t.Errorf("haiku bedrock -> %s", m)
	}
	m, _ = ResolveTieringAndEffort(map[string]interface{}{"model": "bedrock/claude-sonnet-4-5-20250929"}, "", "")
	if m != "gemini-3.8-flash-medium" {
		t.Errorf("sonnet bedrock -> %s", m)
	}
	m, _ = ResolveTieringAndEffort(map[string]interface{}{"model": "gpt-4o"}, "", "")
	if m != "gemini-3.8-flash-tiered" {
		t.Errorf("unknown -> %s", m)
	}
}

func TestCompactionWorkflowTier(t *testing.T) {
	th := map[string]interface{}{"type": "adaptive"}
	for _, class := range []string{"compaction", "workflow"} {
		m, b := ResolveTieringAndEffort(map[string]interface{}{"model": "claude-opus-4-5", "thinking": th}, class, "")
		if m != "gemini-3.8-flash-medium" || b != 8192 {
			t.Errorf("%s with thinking: %s/%d", class, m, b)
		}
		m, b = ResolveTieringAndEffort(map[string]interface{}{"model": "claude-opus-4-5"}, class, "")
		if m != "gemini-3.8-flash-medium" || b != 0 {
			t.Errorf("%s without thinking: %s/%d", class, m, b)
		}
	}
}

func TestClassifyRequestClass(t *testing.T) {
	if got := ClassifyRequestClass("compaction", "x", 10, false, ""); got != "compaction" {
		t.Errorf("explicit class must win, got %q", got)
	}
	if got := ClassifyRequestClass("", "claude-haiku-4-5", 32, false, "Generate a concise title"); got != "auxiliary" {
		t.Errorf("title request = %q", got)
	}
	if got := ClassifyRequestClass("", "claude-sonnet-4-5", 200, false, "Write a sentence-case title for this"); got != "auxiliary" {
		t.Errorf("marker request = %q", got)
	}
	if got := ClassifyRequestClass("", "claude-sonnet-4-5", 200, true, "Generate a concise title"); got != "" {
		t.Errorf("tools present must not be auxiliary, got %q", got)
	}
	if got := ClassifyRequestClass("", "claude-sonnet-4-5", 8000, false, "You are a coding agent"); got != "" {
		t.Errorf("normal request = %q", got)
	}
}
