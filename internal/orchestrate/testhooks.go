// Part of the orchestrate package — see pipeline.go for the package doc
// comment. Test seam for packages that drive the whole handler stack
// (internal/httpapi end-to-end tests) against a fake Gemini upstream.
package orchestrate

import "net/http"

// SetTransportForTest replaces the Google-facing round tripper and returns
// a function restoring the previous one. Not for production use.
func SetTransportForTest(rt http.RoundTripper) func() {
	orig := httpClient.Transport
	httpClient.Transport = rt
	return func() { httpClient.Transport = orig }
}
