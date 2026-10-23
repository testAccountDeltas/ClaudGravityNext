// Package jsondiff is the single owner of "diff two decoded JSON values,
// report only JSON-pointer paths, never raw values" and "redact long
// free-text leaves before comparing" — logic that cmd/shadowcompare
// built first (comparing v1's real captured gemini_request against v2's
// own replayed translation) and pkg/speccheck/rules' rule 13
// (TranslateStructuralParity) now needs too, for exactly the same
// comparison run continuously instead of by hand. Extracted here rather
// than duplicated, same principle as internal/sessionkey/internal/
// pathkey/internal/b64util earlier in this migration: one package, two
// consumers, neither copy drifts out of sync with the other.
package jsondiff

import (
	"fmt"
	"reflect"
	"strings"
)

// RedactThreshold mirrors internal/speccapture's own redaction threshold
// exactly (and superrelay (v1)'s speccapture.go before it) — a string
// this long or longer is replaced with a length-preserving placeholder
// before it's ever written to request_captures/*.ndjson. Diffing v2's
// freshly computed (unredacted) output against that already-redacted
// ground truth without applying the SAME redaction first would report a
// spurious mismatch on every real generative-text field (system prompt,
// message text, tool descriptions, thought signatures) regardless of
// whether the translation itself is correct — found and diagnosed by
// hand against real traffic before this package existed; see
// ../superrelay/ROADMAP_PROGRESS.md's Phase 5 shadowcompare
// write-ups for the full incident history.
const RedactThreshold = 200

// Redact walks v (already decoded via encoding/json, so maps/slices/
// strings/float64/bool/nil only) and replaces any string of
// RedactThreshold characters or more with the same placeholder format
// internal/speccapture writes.
func Redact(v interface{}) interface{} {
	switch t := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(t))
		for k, val := range t {
			out[k] = Redact(val)
		}
		return out
	case []interface{}:
		out := make([]interface{}, len(t))
		for i, val := range t {
			out[i] = Redact(val)
		}
		return out
	case string:
		if len(t) > RedactThreshold {
			return fmt.Sprintf("<redacted text:%d chars>", len(t))
		}
		return t
	default:
		return v
	}
}

// Diff walks a and b in lockstep and appends a JSON-pointer path to *out
// for every point where they disagree. A leaf-value mismatch (both sides
// present, same type, different value — e.g. two different redacted-
// length placeholders, or genuinely different real text) is reported as
// a bare path with no suffix. A STRUCTURAL mismatch (a key present on
// one side only, an array length mismatch, or a type mismatch) gets an
// explicit annotation instead — callers that only care about structural
// regressions (a key silently disappearing, an array growing/shrinking)
// and want to ignore expected leaf-value noise (redaction placeholders,
// real text that legitimately changed) can select on that suffix; see
// IsStructural.
func Diff(path string, a, b interface{}, out *[]string) {
	switch av := a.(type) {
	case map[string]interface{}:
		bv, ok := b.(map[string]interface{})
		if !ok {
			*out = append(*out, path+" (type: object vs "+typeName(b)+")")
			return
		}
		seen := make(map[string]bool, len(av))
		for k, aval := range av {
			seen[k] = true
			p := path + "/" + escapePointer(k)
			bval, present := bv[k]
			if !present {
				*out = append(*out, p+" (missing in v2)")
				continue
			}
			Diff(p, aval, bval, out)
		}
		for k := range bv {
			if !seen[k] {
				*out = append(*out, path+"/"+escapePointer(k)+" (extra in v2)")
			}
		}
	case []interface{}:
		bv, ok := b.([]interface{})
		if !ok {
			*out = append(*out, path+" (type: array vs "+typeName(b)+")")
			return
		}
		if len(av) != len(bv) {
			*out = append(*out, fmt.Sprintf("%s (array length %d vs %d)", path, len(av), len(bv)))
		}
		n := len(av)
		if len(bv) < n {
			n = len(bv)
		}
		for i := 0; i < n; i++ {
			Diff(fmt.Sprintf("%s/%d", path, i), av[i], bv[i], out)
		}
	default:
		if !reflect.DeepEqual(a, b) {
			*out = append(*out, path)
		}
	}
}

// IsStructural reports whether a path returned by Diff carries one of
// the explicit structural annotations (missing/extra/type/array length)
// rather than being a bare leaf-value mismatch.
func IsStructural(path string) bool {
	return strings.HasSuffix(path, " (missing in v2)") ||
		strings.HasSuffix(path, " (extra in v2)") ||
		strings.Contains(path, " (type: ") ||
		strings.Contains(path, " (array length ")
}

func typeName(v interface{}) string {
	switch v.(type) {
	case map[string]interface{}:
		return "object"
	case []interface{}:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// escapePointer applies RFC 6901's two required escapes for a JSON
// Pointer path segment.
func escapePointer(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	return s
}
