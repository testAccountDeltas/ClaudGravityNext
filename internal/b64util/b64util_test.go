package b64util

import (
	"encoding/base64"
	"testing"
)

func TestNormalizeBase64_Padding(t *testing.T) {
	// rem == 0: already valid 4-byte block
	if got := NormalizeBase64("YWJj"); got != "YWJj" {
		t.Fatalf("expected YWJj, got %q", got)
	}

	// rem == 3: needs 1 '='
	got3 := NormalizeBase64("YWI")
	if got3 != "YWI=" {
		t.Fatalf("expected YWI=, got %q", got3)
	}
	if _, err := base64.StdEncoding.DecodeString(got3); err != nil {
		t.Fatalf("failed to decode rem==3 padded base64: %v", err)
	}

	// rem == 2: needs 2 '='
	got2 := NormalizeBase64("YQ")
	if got2 != "YQ==" {
		t.Fatalf("expected YQ==, got %q", got2)
	}
	if _, err := base64.StdEncoding.DecodeString(got2); err != nil {
		t.Fatalf("failed to decode rem==2 padded base64: %v", err)
	}

	// rem == 1: orphan char, must be trimmed rather than padded with ===
	// "YWJj" decodes to "abc". "YWJjX" has rem==1. Trimming 'X' gives "YWJj".
	got1 := NormalizeBase64("YWJjX")
	if got1 != "YWJj" {
		t.Fatalf("expected trimmed YWJj, got %q", got1)
	}
	if decoded, err := base64.StdEncoding.DecodeString(got1); err != nil || string(decoded) != "abc" {
		t.Fatalf("failed to decode rem==1 trimmed base64: %v (decoded=%q)", err, string(decoded))
	}
}

func TestNormalizeBase64_URLSafeAndDataURIAndWhitespace(t *testing.T) {
	input := "  data:image/png;base64, Y-J_ \n"
	// Y-J_ -> whitespace stripped: Y-J_ -> URL safe replaced: Y+J/ -> len 4, rem 0
	got := NormalizeBase64(input)
	if got != "Y+J/" {
		t.Fatalf("expected Y+J/, got %q", got)
	}
}
