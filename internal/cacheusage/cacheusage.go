// Package cacheusage emulates Anthropic prompt-caching usage accounting on
// top of an upstream (Gemini) that reports only cachedContentTokenCount.
// It never invents numbers: cache_creation tokens are reported only when
// the client actually placed cache_control breakpoints, nothing was read
// from cache, and the prefix before the last breakpoint reaches the
// model's documented minimum cacheable size.
package cacheusage

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// MaxBreakpoints is Anthropic's documented limit of cache_control
// breakpoints per request.
const MaxBreakpoints = 4

// Info describes the cache_control breakpoints found in a request body.
type Info struct {
	Count      int     // ephemeral breakpoints
	PrefixFrac float64 // byte-offset proxy of the prefix up to the last breakpoint
	Frac1h     float64 // same, up to the last breakpoint with ttl "1h" (0 if none)
}

var cacheKey = []byte(`"cache_control"`)

// Analyze scans the raw Claude request body for cache_control breakpoints.
// Byte offsets are used as a proxy for token position (Gemini reports no
// per-breakpoint split) — coarse but honest.
func Analyze(body []byte) Info {
	var info Info
	if len(body) == 0 {
		return info
	}
	pos := 0
	for {
		i := bytes.Index(body[pos:], cacheKey)
		if i < 0 {
			break
		}
		abs := pos + i
		pos = abs + len(cacheKey)
		if abs > 0 && body[abs-1] == 0x5c { // escaped inside a JSON string
			continue
		}
		rest := bytes.TrimLeft(body[pos:], " \t\r\n")
		if len(rest) == 0 || rest[0] != ':' {
			continue
		}
		val := gjson.ParseBytes(bytes.TrimLeft(rest[1:], " \t\r\n"))
		if !val.IsObject() || val.Get("type").String() != "ephemeral" {
			continue
		}
		info.Count++
		frac := clamp(float64(abs) / float64(len(body)))
		info.PrefixFrac = frac
		if val.Get("ttl").String() == "1h" {
			info.Frac1h = frac
		}
	}
	return info
}

func clamp(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

var modelRe = regexp.MustCompile(`(opus|sonnet|haiku)-(\d+)(?:[-.](\d+))?`)

// UpstreamMinTokens is the smallest prefix Gemini's implicit cache actually
// serves (measured live: ~3.5k tokens never hit, ~5.8k hit). Reporting a
// cache write below it would promise a read that never happens.
const UpstreamMinTokens = 4096

// MinTokens returns the minimum cacheable prefix length for a model
// (Anthropic prompt-caching docs): 4096 for Opus 4.5+ and Haiku 4.5+,
// 2048 for Sonnet 4.6+ and older Haiku, 1024 otherwise.
func MinTokens(model string) int64 {
	m := modelRe.FindStringSubmatch(strings.ToLower(model))
	if m == nil {
		return 1024
	}
	major, minor := atoi(m[2]), atoi(m[3])
	atLeast := func(maj, min int) bool { return major > maj || (major == maj && minor >= min) }
	switch m[1] {
	case "opus":
		if atLeast(4, 5) {
			return 4096
		}
	case "sonnet":
		if atLeast(4, 6) {
			return 2048
		}
	case "haiku":
		if atLeast(4, 5) {
			return 4096
		}
		return 2048
	}
	return 1024
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

// Split returns the cache-write tokens (5m / 1h) for a turn. uncached is
// the prompt size not served from cache; cacheRead is what upstream
// reported as cached. Both results are 0 unless a breakpoint exists,
// nothing was read from cache and the prefix reaches the model minimum.
func Split(model string, uncached, cacheRead int64, frac, frac1h float64) (tok5m, tok1h int64) {
	if frac <= 0 || cacheRead > 0 || uncached <= 0 {
		return 0, 0
	}
	prefix := int64(float64(uncached) * frac)
	if prefix < MinTokens(model) || prefix < UpstreamMinTokens {
		return 0, 0
	}
	tok1h = int64(float64(uncached) * frac1h)
	if tok1h > prefix {
		tok1h = prefix
	}
	return prefix - tok1h, tok1h
}

// Object builds the usage.cache_creation breakdown object.
func Object(tok5m, tok1h int64) map[string]interface{} {
	return map[string]interface{}{
		"ephemeral_5m_input_tokens": tok5m,
		"ephemeral_1h_input_tokens": tok1h,
	}
}
