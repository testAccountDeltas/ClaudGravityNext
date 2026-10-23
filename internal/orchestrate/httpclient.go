// Part of the orchestrate package — see pipeline.go for the package doc
// comment.
package orchestrate

import (
	"net/http"

	"claudgravity/internal/tlsfingerprint"
)

// httpClient is this package's own Google-facing client — a legitimate,
// dedicated dependency on internal/tlsfingerprint (orchestrate really
// does make the outbound inference call) rather than a shared global
// reached across package boundaries, same rationale as
// internal/accounts' own copy.
var httpClient = &http.Client{
	Transport: tlsfingerprint.NewFingerprintedRoundTripper(),
}
