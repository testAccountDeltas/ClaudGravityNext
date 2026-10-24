// Package testutil holds fakes shared by the gateway's tests: a function
// round tripper, Gemini SSE/JSON builders and an on-disk account pool.
package testutil

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claudgravity/internal/accounts"
	"claudgravity/internal/config"
)

// RoundTripFunc adapts a function to http.RoundTripper.
type RoundTripFunc func(*http.Request) (*http.Response, error)

func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Response builds a response with the given status and body.
func Response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

// SeedAccounts writes n fake OAuth account files into a temp dir, stubs
// the token-refresh endpoint and initialises the account manager. The
// returned restore func is registered with t.Cleanup already.
func SeedAccounts(t *testing.T, n int) {
	t.Helper()
	restore := accounts.SetHTTPClientForTest(&http.Client{Transport: RoundTripFunc(func(*http.Request) (*http.Response, error) {
		return Response(200, `{"access_token":"fresh","expires_in":3600}`), nil
	})})
	t.Cleanup(restore)
	t.Cleanup(accounts.ResetForTest)
	dir := t.TempDir()
	for i := 0; i < n; i++ {
		e := fmt.Sprintf("tu-%d@example.com", i)
		body := fmt.Sprintf(`{"email":%q,"access_token":"token-%d","refresh_token":"ref%d"}`, e, i+1, i+1)
		if err := os.WriteFile(filepath.Join(dir, e+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := accounts.InitAccountManager(dir, config.ServerConfig{}); err != nil {
		t.Fatal(err)
	}
}

// GeminiChunk wraps one candidate in the Cloud Code response envelope.
func GeminiChunk(candidate string, promptTokens, outTokens int) string {
	return fmt.Sprintf(`{"response":{"candidates":[%s],"usageMetadata":{"promptTokenCount":%d,"candidatesTokenCount":%d}}}`, candidate, promptTokens, outTokens)
}

// TextCandidate is a model text part, optionally with a finishReason.
func TextCandidate(text, finishReason string) string {
	fr := ""
	if finishReason != "" {
		fr = fmt.Sprintf(`,"finishReason":%q`, finishReason)
	}
	return fmt.Sprintf(`{"content":{"role":"model","parts":[{"text":%q}]}%s}`, text, fr)
}

// SSE joins chunks into a Gemini streaming body.
func SSE(chunks ...string) string {
	var b strings.Builder
	for _, c := range chunks {
		b.WriteString("data: " + c + "\n\n")
	}
	return b.String()
}
