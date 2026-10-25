package b64util

import "testing"

func TestCov_StripDataURIPrefix(t *testing.T) {
	cases := map[string]string{
		"plain":                           "plain",
		"data:image/png;base64,AAAA":      "AAAA",
		"  \n DATA:image/png;base64,BB==": "BB==",
		"data:text/plain,hello":           "hello", // no ;base64, marker: cut at first comma
		"data:nocomma":                    "data:nocomma",
		"":                                "",
		"  trimmed  ":                     "trimmed",
	}
	for in, want := range cases {
		if got := StripDataURIPrefix(in); got != want {
			t.Errorf("StripDataURIPrefix(%q)=%q want %q", in, got, want)
		}
	}
}
