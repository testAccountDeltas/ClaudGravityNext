package mitm

import (
	"bytes"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const tkey = "gw-tenant-key"

func withMatcher(t *testing.T) {
	t.Helper()
	SetSecretMatcher(func(s string) bool { return s == tkey || s == "proxy-sec" })
	t.Cleanup(func() { SetSecretMatcher(nil) })
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func TestScrubCredentialHeaders_Table(t *testing.T) {
	withMatcher(t)
	cases := []struct {
		name, header, value string
		wantGone            bool
	}{
		{"bearer", "Authorization", "Bearer " + tkey, true},
		{"bearer lowercase scheme", "Authorization", "bearer " + tkey, true},
		{"bearer upper scheme", "Authorization", "BEARER " + tkey, true},
		{"bare key", "Authorization", tkey, true},
		{"basic secret as user", "Authorization", "Basic " + b64(tkey+":x"), true},
		{"basic secret as password", "Authorization", "Basic " + b64("x:"+tkey), true},
		{"basic secret only", "Authorization", "basic " + b64(tkey), true},
		{"basic proxy secret", "Authorization", "Basic " + b64(":proxy-sec"), true},
		{"basic unrelated", "Authorization", "Basic " + b64("alice:pw"), false},
		{"basic bad base64", "Authorization", "Basic !!!", false},
		{"foreign bearer", "Authorization", "Bearer sk-ant-real", false},
		{"x-api-key secret", "X-Api-Key", tkey, true},
		{"x-api-key foreign", "X-Api-Key", "sk-ant-real", false},
		{"cookie secret", "Cookie", "a=b; sid=" + tkey + "; c=d", true},
		{"cookie foreign", "Cookie", "a=b; sid=zzz", false},
		{"proxy-authorization always dropped", "Proxy-Authorization", "Basic " + b64("u:p"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			h.Set(tc.header, tc.value)
			scrubCredentialHeaders(h, true)
			if gone := h.Get(tc.header) == ""; gone != tc.wantGone {
				t.Fatalf("gone=%v want %v (left %q)", gone, tc.wantGone, h.Get(tc.header))
			}
		})
	}
}

func TestScrubCredentialHeaders_MultiValuedKeepsForeign(t *testing.T) {
	withMatcher(t)
	h := http.Header{}
	h.Add("Authorization", "Bearer sk-ant-real")
	h.Add("Authorization", "Basic "+b64("x:"+tkey))
	h.Add("X-Api-Key", tkey)
	h.Add("X-Api-Key", "sk-ant-other")
	scrubCredentialHeaders(h, true)
	if v := h.Values("Authorization"); len(v) != 1 || v[0] != "Bearer sk-ant-real" {
		t.Fatalf("Authorization = %v", v)
	}
	if v := h.Values("X-Api-Key"); len(v) != 1 || v[0] != "sk-ant-other" {
		t.Fatalf("X-Api-Key = %v", v)
	}
}

func TestScrubQuery_Table(t *testing.T) {
	withMatcher(t)
	cases := []struct{ in, want string }{
		{"", ""},
		{"key=" + tkey, ""},
		{"a=1&key=" + tkey + "&b=2", "a=1&b=2"},
		{"api_key=" + tkey, ""},
		{"a=1&b=2", "a=1&b=2"},
		{"key=sk-ant-real", "key=sk-ant-real"},
		{"a=%zz&k=" + tkey, ""}, // unparseable but mentions the secret
	}
	for _, tc := range cases {
		if got := scrubQuery(tc.in); got != tc.want {
			t.Errorf("scrubQuery(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPassthrough_QueryKeyNeverForwarded(t *testing.T) {
	withMatcher(t)
	orig := passthroughClient
	t.Cleanup(func() { passthroughClient = orig })
	var gotURL string
	passthroughClient = &http.Client{Transport: testRoundTripper(func(req *http.Request) (*http.Response, error) {
		gotURL = req.URL.String()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	req := httptest.NewRequest("GET", "http://api.anthropic.com/x?key="+tkey+"&keep=1", nil)
	req.Host = "api.anthropic.com"
	PassthroughToReal(httptest.NewRecorder(), req)
	if strings.Contains(gotURL, tkey) || !strings.Contains(gotURL, "keep=1") {
		t.Fatalf("forwarded URL %q", gotURL)
	}
}

func TestPassthrough_BodyCap(t *testing.T) {
	withMatcher(t)
	orig := passthroughClient
	t.Cleanup(func() { passthroughClient = orig })
	called := false
	passthroughClient = &http.Client{Transport: testRoundTripper(func(req *http.Request) (*http.Response, error) {
		called = true
		_, _ = io.Copy(io.Discard, req.Body)
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	run := func(n int) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "http://api.anthropic.com/x", bytes.NewReader(make([]byte, n)))
		req.Host = "api.anthropic.com"
		rec := httptest.NewRecorder()
		PassthroughToReal(rec, req)
		return rec
	}
	if rec := run(MaxPassthroughBodyBytes); rec.Code != 200 || !called {
		t.Fatalf("exact cap: %d called=%v", rec.Code, called)
	}
	called = false
	rec := run(MaxPassthroughBodyBytes + 1)
	if rec.Code != http.StatusRequestEntityTooLarge || called || !strings.Contains(rec.Body.String(), "request_too_large") {
		t.Fatalf("cap+1: %d called=%v body=%s", rec.Code, called, rec.Body.String())
	}
}
