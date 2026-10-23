// Part of the config package's port from superrelay (v1)'s config.go —
// see config.go in this directory for the split rationale. This file
// holds only ServerConfig and its two derived-default helpers.
package config

// ServerConfig is the ["server"] section of config.json.
type ServerConfig struct {
	Port                            int    `json:"port"`
	PanicTimeoutSeconds             int    `json:"panic_timeout_seconds"`
	TTFTTimeoutSeconds              int    `json:"ttft_timeout_seconds"`
	ReadTimeoutSeconds              int    `json:"read_timeout_seconds"`
	TLSPort                         int    `json:"tls_port"`                                       // HTTPS listener for ANTHROPIC_BASE_URL (CA-signed, separate from the plain CONNECT-proxy port)
	PublicHost                      string `json:"public_host,omitempty"`                          // hostname/IP clients will actually connect to (e.g. a remote server's IP); included as a cert SAN so TLS hostname verification passes. Defaults to "localhost".
	DashboardPassword               string `json:"dashboard_password"`                             // HTTP Basic Auth password for the web UI (username: admin); auto-generated on first run
	ProxySecret                     string `json:"proxy_secret"`                                   // required as Proxy-Authorization bearer on CONNECT requests; auto-generated on first run
	GoogleOAuthClientID             string `json:"google_oauth_client_id,omitempty"`               // overrides internal/accounts' default Antigravity OAuth client id if set — applied by the accounts package's own init wiring, NOT by this package (see config.go's LoadConfig doc comment for why)
	GoogleOAuthClientSecret         string `json:"google_oauth_client_secret,omitempty"`           // overrides internal/accounts' default Antigravity OAuth client secret if set — same caveat as above
	MaxStreamDurationSeconds        int    `json:"max_stream_duration_seconds,omitempty"`          // absolute safety-net ceiling for one streaming request, regardless of ongoing activity; default 1800s (30min) if unset. The real per-request cutoff is idle silence > ttft_timeout_seconds, not this.
	AntigravityUserAgent            string `json:"antigravity_user_agent,omitempty"`               // overrides internal/accounts' default Antigravity client User-Agent if set — bump this when Google ships a new real Antigravity client version
	SpecCaptureEnabled              *bool  `json:"spec_capture_enabled,omitempty"`                 // records a redacted (no generative text) structural skeleton of every request/response for cmd/speccheck — see internal/speccapture. nil (field absent) defaults to true; a plain bool default couldn't tell "absent" from "false".
	SpecCaptureDir                  string `json:"spec_capture_dir,omitempty"`                     // defaults to "request_captures" relative to the working directory
	SpecCaptureRetentionDays        int    `json:"spec_capture_retention_days,omitempty"`          // defaults to 14; capture files are named by UTC date and pruned on each day rotation
	SpecCaptureRawChunksEnabled     *bool  `json:"spec_capture_raw_chunks_enabled,omitempty"`      // records UNREDACTED raw Gemini streaming chunks under <dir>/raw_chunks/, for the StreamFSM replay corpus. nil (absent) defaults to FALSE — unlike the redacted capture above, this keeps real generative text, so it's opt-in, meant to be switched on only long enough to accumulate a corpus.
	SpecCaptureRawChunksMaxSessions int    `json:"spec_capture_raw_chunks_max_sessions,omitempty"` // defaults to 100; oldest raw_chunks/*.ndjson files are deleted once this count is exceeded
	AllowHistoryTruncation          bool   `json:"allow_history_truncation,omitempty"`             // opt-in: silently drop the oldest turns when a prompt exceeds the context window. Default false: such a request is rejected with Anthropic's "prompt is too long: N tokens > M maximum" 400 so the client can compact.
	SpecCaptureRawChunksMaxBytes    int64  `json:"spec_capture_raw_chunks_max_bytes,omitempty"`    // defaults to 50MB; oldest raw_chunks/*.ndjson files are deleted once the directory exceeds this total size
}

// SpecCaptureIsEnabled resolves the nil-means-true default for
// SpecCaptureEnabled described above.
func (s ServerConfig) SpecCaptureIsEnabled() bool {
	return s.SpecCaptureEnabled == nil || *s.SpecCaptureEnabled
}

// SpecCaptureRawChunksIsEnabled resolves the nil-means-FALSE default for
// SpecCaptureRawChunksEnabled described above — the opposite default
// polarity from SpecCaptureIsEnabled, deliberately, since this capture is
// unredacted.
func (s ServerConfig) SpecCaptureRawChunksIsEnabled() bool {
	return s.SpecCaptureRawChunksEnabled != nil && *s.SpecCaptureRawChunksEnabled
}
