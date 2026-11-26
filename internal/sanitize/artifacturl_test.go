package sanitize

import (
	"testing"
)

func TestRewriteArtifactURLs(t *testing.T) {
	base := "http://203.0.113.10:9101/"
	cases := []struct {
		in, want string
	}{
		{"plain response", "plain response"},
		{"see https://claude.ai/login for auth", "see https://claude.ai/login for auth"},
		{
			"Вот ссылка: https://claude.ai/code/artifact/2bb7b152-e5f1-40ce-acc9-66838ca1da1c готово",
			"Вот ссылка: http://203.0.113.10:9101/code/artifact/2bb7b152-e5f1-40ce-acc9-66838ca1da1c готово",
		},
		{
			"short: https://claude.ai/artifact/2bb7b152-e5f1-40ce-acc9-66838ca1da1c",
			"short: http://203.0.113.10:9101/artifact/2bb7b152-e5f1-40ce-acc9-66838ca1da1c",
		},
	}
	for _, c := range cases {
		if got := RewriteArtifactURLs(c.in, base); got != c.want {
			t.Errorf("RewriteArtifactURLs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if allocs := testing.AllocsPerRun(100, func() {
		_ = RewriteArtifactURLs("обычный длинный текст без целевых ссылок", base)
		_ = PartialArtifactURLSuffix("обычный длинный текст без целевых ссылок")
	}); allocs != 0 {
		t.Fatalf("fast-path must allocate 0, got %v", allocs)
	}
}

func TestPartialArtifactURLSuffix(t *testing.T) {
	if got := PartialArtifactURLSuffix("Вот: https://claude.ai/code/"); got != "https://claude.ai/code/" {
		t.Fatalf("got %q", got)
	}
	if got := PartialArtifactURLSuffix("hello h"); got != "h" {
		t.Fatalf("got %q", got)
	}
	if got := PartialArtifactURLSuffix("https://claude.ai/settings"); got != "" {
		t.Fatalf("non-artifact url should not hold, got %q", got)
	}
	if got := PartialArtifactURLSuffix("https://claude.ai/code/artifact/abc"); got != "" {
		t.Fatalf("complete prefix should not hold, got %q", got)
	}
}
