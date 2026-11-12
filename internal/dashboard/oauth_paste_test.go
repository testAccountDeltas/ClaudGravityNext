package dashboard

import "testing"

func TestParseOAuthRedirect(t *testing.T) {
	cases := []struct {
		in          string
		state, code string
		ok          bool
	}{
		{"http://localhost:8898/oauth/callback?state=s1&code=4%2F0abc&scope=x", "s1", "4/0abc", true},
		{"  state=s2&code=c2 \n", "s2", "c2", true},
		{"http://localhost:8898/oauth/callback?state=s3", "", "", false},
		{"http://localhost:8898/oauth/callback?code=c4", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		s, code, ok := parseOAuthRedirect(c.in)
		if ok != c.ok || (ok && (s != c.state || code != c.code)) {
			t.Errorf("parseOAuthRedirect(%q) = %q,%q,%v; want %q,%q,%v", c.in, s, code, ok, c.state, c.code, c.ok)
		}
	}
}
