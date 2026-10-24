package accounts

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClassifyError_Table(t *testing.T) {
	cases := []struct {
		status int
		text   string
		want   string
	}{
		{400, "OAuth invalid_grant: revoked", ErrKindTokenRevoked},
		{0, "no refresh token", ErrKindTokenRevoked},
		{401, "unauthorized_client", ErrKindTokenRevoked},
		{403, "banned", ErrKindForbidden},
		{401, "token expired", ErrKindAuthExpired},
		{429, "refresh rate-limited (429): slow down", ErrKindRateLimit},
		{429, "quota", ErrKindQuota},
		{200, "RESOURCE_EXHAUSTED: out", ErrKindQuota},
		{503, "x", ErrKindOverloaded},
		{529, "x", ErrKindOverloaded},
		{500, "server Overloaded", ErrKindOverloaded},
		{0, "dial tcp: connection refused", ErrKindNetwork},
		{0, "context deadline exceeded", ErrKindNetwork},
		{0, "lookup x: no such host", ErrKindNetwork},
		{0, "unexpected EOF", ErrKindNetwork},
		{500, "connection reset", ErrKindOther}, // network text only counts when status==0
		{418, "teapot", ErrKindOther},
		{0, "", ErrKindOther},
	}
	for _, c := range cases {
		if got := ClassifyError(c.status, c.text); got != c.want {
			t.Errorf("ClassifyError(%d,%q)=%q want %q", c.status, c.text, got, c.want)
		}
	}
}

func TestStatusFromErr_Table(t *testing.T) {
	cases := map[string]int{
		"HTTP 403 from host: banned":           403,
		"refresh failed (400): bad":            400,
		"refresh rate-limited (429): slow":     429,
		"wrapped: HTTP 503 from x":             503,
		"HTTP 99 from x":                       0, // < 100 is not a status
		"HTTP abc":                             0,
		"plain error":                          0,
		"refresh for a recently failed, retry": 0,
	}
	for msg, want := range cases {
		if got := statusFromErr(errors.New(msg)); got != want {
			t.Errorf("statusFromErr(%q)=%d want %d", msg, got, want)
		}
	}
}

func TestStatusFromErr_RateLimitedClassifiesAsRateLimit(t *testing.T) {
	err := errors.New("refresh rate-limited (429): quota")
	if k := ClassifyError(statusFromErr(err), err.Error()); k != ErrKindRateLimit {
		t.Fatalf("got %q", k)
	}
}

func TestRecordAccountError_StoresFieldsAndTruncatesRuneSafe(t *testing.T) {
	acc := &Record{Email: "h@example.com"}
	seedAccounts(t, acc)
	// Cyrillic letters are 2 bytes: a 2-byte rune straddles byte 300.
	text := strings.Repeat("a", 299) + "я" + "tail"
	globalAccountManager.RecordAccountError(acc.Email, 429, text, acc.CooldownUntil)
	if !utf8.ValidString(acc.ErrorDetail) {
		t.Fatalf("ErrorDetail split a rune: %q", acc.ErrorDetail)
	}
	if len(acc.ErrorDetail) != 299 {
		t.Fatalf("want 299 bytes (rune dropped whole), got %d", len(acc.ErrorDetail))
	}
	if acc.ErrorKind != ErrKindQuota || acc.ErrorAt.IsZero() {
		t.Fatalf("kind/at not stored: %+v", acc)
	}
	globalAccountManager.RecordAccountError("ghost@example.com", 500, "x", acc.CooldownUntil) // unknown: no panic
}

func TestTruncateRunes(t *testing.T) {
	inputs := []string{"", "abc", "日本語日本語日本語", "😀😀😀😀", strings.Repeat("я", 400)}
	for _, s := range inputs {
		for _, n := range []int{0, 1, 2, 3, 4, 5, 300} {
			got := truncateRunes(s, n)
			if len(got) > n || !utf8.ValidString(got) || !strings.HasPrefix(s, got) {
				t.Errorf("truncateRunes(%q,%d)=%q", s, n, got)
			}
		}
	}
	if truncateRunes("exact", 5) != "exact" {
		t.Fatal("must not cut a string that fits")
	}
}
