package mitm

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func capturePassthrough(t *testing.T, host string, hdr map[string]string) http.Header {
	t.Helper()
	orig := passthroughClient
	defer func() { passthroughClient = orig }()
	var got http.Header
	passthroughClient = &http.Client{Transport: testRoundTripper(func(req *http.Request) (*http.Response, error) {
		got = req.Header.Clone()
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}
	req := httptest.NewRequest(http.MethodHead, "http://"+host+"/api/hello", nil)
	req.Host = host
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	PassthroughToReal(httptest.NewRecorder(), req)
	return got
}

func TestPassthroughNeverForwardsGatewaySecrets(t *testing.T) {
	SetSecretMatcher(func(s string) bool { return s == "gw-tenant-key" })
	defer SetSecretMatcher(nil)

	// Plain (non-Anthropic) host: every credential is dropped.
	h := capturePassthrough(t, "localhost:8900", map[string]string{
		"Authorization": "Bearer gw-tenant-key", "x-api-key": "gw-tenant-key", "Cookie": "a=b",
	})
	for _, k := range []string{"Authorization", "X-Api-Key", "Cookie"} {
		if h.Get(k) != "" {
			t.Errorf("%s leaked to upstream on non-Anthropic host: %q", k, h.Get(k))
		}
	}

	// Anthropic host: the gateway key is still dropped, a foreign credential survives.
	h = capturePassthrough(t, "api.anthropic.com", map[string]string{
		"Authorization": "Bearer gw-tenant-key", "x-api-key": "sk-ant-real",
	})
	if h.Get("Authorization") != "" {
		t.Errorf("gateway key forwarded to Anthropic: %q", h.Get("Authorization"))
	}
	if h.Get("x-api-key") != "sk-ant-real" {
		t.Errorf("foreign credential should be preserved, got %q", h.Get("x-api-key"))
	}
}
