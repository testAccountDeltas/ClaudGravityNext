// Package speccapture is a port of superrelay (v1)'s speccapture.go,
// split across four files by responsibility per
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 4.3: redact.go
// (this file — the string-length redaction rule), capture.go (the store
// type + the four Record*/Begin/SetSessionMeta leg-capture methods),
// rawchunks.go (the unredacted replay-corpus capture added this same
// migration session), finish.go (AppendClaudeEvent/Finish/writeLine/
// pruneOld — assembling and persisting one finished request's NDJSON
// line). Every function body is unchanged from v1.
package speccapture

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// specRedactThreshold is the only rule deciding what counts as "generative
// text" worth stripping: any JSON string longer than this many characters
// is almost certainly prose, a tool-result body, or a base64 blob — never a
// role/type/model/tool name, a stop_reason, a header value, or an id, all
// of which are comfortably shorter. A hardcoded per-field name/path
// allowlist would need to be kept in sync with both Claude's and Gemini's
// evolving schemas forever; a length threshold doesn't.
const specRedactThreshold = 200

// redactSkeleton recursively strips long string VALUES, keeping every map
// key, every short string (the actual structure/enums/ids we care about),
// every number, bool, and null, and the shape (object/array nesting, array
// lengths) fully intact.
func redactSkeleton(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[k] = redactSkeleton(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = redactSkeleton(val)
		}
		return out
	case string:
		if len(t) > specRedactThreshold {
			return fmt.Sprintf("<redacted text:%d chars>", len(t))
		}
		return t
	default:
		return v
	}
}

// redactJSONBytes parses raw JSON and redacts it. A body that fails to
// parse (never expected, but this must never panic/crash a live request
// over a capture-side bug) is recorded as just its length.
func redactJSONBytes(raw []byte) interface{} {
	if len(raw) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(raw, &v); err != nil {
		return map[string]interface{}{"_unparsed_len": len(raw)}
	}
	return redactSkeleton(v)
}

// isSpecSensitiveHeader flags headers whose VALUE is a credential, never
// structure — these get stored as presence+length only, same spirit as
// requestlog.MaskKey for the dashboard's own tenant-key display.
func isSpecSensitiveHeader(name string) bool {
	l := strings.ToLower(name)
	if l == "authorization" || l == "x-api-key" || l == "proxy-authorization" {
		return true
	}
	// Deliberately NOT a bare "token" substring match — that also matches
	// the entirely non-sensitive anthropic-ratelimit-tokens-limit/
	// -remaining/-reset headers (rate-limit NUMBERS, not credentials),
	// hiding exactly the structural data this capture exists to keep.
	return strings.Contains(l, "secret") || strings.Contains(l, "access-token") ||
		strings.Contains(l, "refresh-token") || strings.Contains(l, "bearer")
}

func redactHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		val := strings.Join(v, ", ")
		if isSpecSensitiveHeader(k) {
			out[k] = fmt.Sprintf("<present, %d chars>", len(val))
			continue
		}
		out[k] = val
	}
	return out
}
