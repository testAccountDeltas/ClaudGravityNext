package mitm

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxyAuthOK_Table(t *testing.T) {
	const sec = "s3cret"
	cases := []struct {
		name, secret string
		hdr          map[string]string
		want         bool
	}{
		{"bearer", sec, map[string]string{"Proxy-Authorization": "Bearer " + sec}, true},
		{"bearer mixed case scheme", sec, map[string]string{"Proxy-Authorization": "bEaReR " + sec}, true},
		{"bearer extra spaces", sec, map[string]string{"Proxy-Authorization": "Bearer   " + sec + "  "}, true},
		{"bearer wrong", sec, map[string]string{"Proxy-Authorization": "Bearer nope"}, false},
		{"basic secret as user", sec, map[string]string{"Proxy-Authorization": "Basic " + b64(sec+":x")}, true},
		{"basic secret as password", sec, map[string]string{"Proxy-Authorization": "Basic " + b64("x:"+sec)}, true},
		{"basic secret no colon", sec, map[string]string{"Proxy-Authorization": "Basic " + b64(sec)}, true},
		{"basic wrong user empty password", sec, map[string]string{"Proxy-Authorization": "Basic " + b64("wrong:")}, false},
		{"basic empty user and password", sec, map[string]string{"Proxy-Authorization": "Basic " + b64(":")}, false},
		{"basic malformed base64", sec, map[string]string{"Proxy-Authorization": "Basic %%%"}, false},
		{"unknown scheme", sec, map[string]string{"Proxy-Authorization": "Digest " + sec}, false},
		{"no header", sec, nil, false},
		{"empty secret", "", map[string]string{"Proxy-Authorization": "Bearer "}, false},
		{"whitespace secret", "  ", map[string]string{"Proxy-Authorization": "Bearer   "}, false},
		{"falls back to Authorization", sec, map[string]string{"Authorization": "Bearer " + sec}, true},
		{"Proxy-Authorization wins over Authorization", sec,
			map[string]string{"Proxy-Authorization": "Bearer bad", "Authorization": "Bearer " + sec}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodConnect, "http://x:443", nil)
			for k, v := range tc.hdr {
				r.Header.Set(k, v)
			}
			if got := ProxyAuthOK(r, tc.secret); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}
