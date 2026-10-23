// Package b64util holds small, pure cleanup helpers for base64 payloads
// that arrive from a client in a shape stricter than Anthropic's own spec
// expects — used by internal/tokens (pricing image/PDF attachments) and,
// once ported, the translate path that actually decodes them. Extracted
// out of superrelay (v1)'s translator.go rather than duplicated, so a
// future translate port reuses this exact copy instead of writing a
// second one that can drift.
package b64util

import "strings"

// StripWhitespace removes embedded whitespace (line wraps, stray
// \r\n/\n/spaces some clients leave in) from a base64 string — some
// clients produce valid, spec-legal line-wrapped base64 that a strict
// decoder on the receiving end rejects outright.
func StripWhitespace(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, s)
}

// StripDataURIPrefix strips a leading "data:<mime>;base64," prefix if
// present, returning only the base64 payload after the comma. Anthropic's
// own API never puts the prefix in a source block's "data" field, but
// some clients pass the full data: URI through anyway, which breaks every
// base64 decoder downstream since the prefix text itself isn't valid
// base64.
func StripDataURIPrefix(s string) string {
	// GAP-D4-01: a leading newline/space before "data:..." (some clients
	// pretty-print or wrap the field) made HasPrefix miss the prefix
	// entirely, leaving it un-stripped and breaking base64 decoding
	// downstream.
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(strings.ToLower(s), "data:") {
		return s
	}
	if idx := strings.Index(s, ";base64,"); idx != -1 {
		return s[idx+len(";base64,"):]
	}
	if idx := strings.Index(s, ","); idx != -1 {
		return s[idx+1:]
	}
	return s
}

// NormalizeBase64 strips prefixes, whitespace, replaces URL-safe characters,
// and ensures valid 4-byte padding with '='.
func NormalizeBase64(s string) string {
	s = StripDataURIPrefix(s)
	s = StripWhitespace(s)
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	if rem := len(s) % 4; rem != 0 {
		if rem == 1 {
			// A single dangling base64 character holds only 6 bits and cannot
			// represent a valid byte under RFC 4648; trimming it avoids illegal "===" padding.
			s = s[:len(s)-1]
		} else {
			s += strings.Repeat("=", 4-rem)
		}
	}
	return s
}
