package stream

import (
	"testing"

	"claudgravity/internal/reqctx"
)

func TestArtifactURLRewrite_SplitAcrossChunks(t *testing.T) {
	p := &reqctx.Params{
		ModelName:         "claude-opus-4-5",
		ArtifactViewerURL: "http://203.0.113.10:9101",
	}
	_, body, err := feedText(p,
		"Готово: https://claude.",
		"ai/code/",
		"artifact/2bb7b152-e5f1-40ce-acc9-66838ca1da1c и ещё https://claude.ai/login",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "Готово: http://203.0.113.10:9101/code/artifact/2bb7b152-e5f1-40ce-acc9-66838ca1da1c и ещё https://claude.ai/login"
	if got := textOf(body); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}
