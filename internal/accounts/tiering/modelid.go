package tiering

import (
	"regexp"
	"strings"
)

var (
	// "-v1:0", "-v2", ":0" style provider version suffixes (Bedrock/Vertex).
	versionSuffixRe = regexp.MustCompile(`(-v\d+(:\d+)?|:\d+)$`)
	// "-20250929" release-date suffix.
	dateSuffixRe = regexp.MustCompile(`-\d{8}$`)
	// "@20250929" Vertex date pin (also "@latest").
	atSuffixRe = regexp.MustCompile(`@[a-z0-9]+$`)
	// trailing context-window selector such as "[1m]".
	bracketRe = regexp.MustCompile(`\[[^\]]*\]$`)
)

var providerPrefixes = []string{
	"us.", "eu.", "apac.", "global.", "au.", "jp.", "ca.",
	"anthropic.", "anthropic/", "bedrock/", "vertex_ai/", "vertex/", "models/",
}

// NormalizeModelID reduces a client-supplied model id to a bare, lowercase
// form: provider prefixes (us./eu./apac./global./anthropic./bedrock/
// vertex_ai/vertex/), Bedrock "-v1:0" suffixes, "@date" pins, "-YYYYMMDD"
// dates, "-latest" and "[1m]" selectors are all stripped. The result is
// what the tier mapping and the /v1/models lookup match against.
func NormalizeModelID(id string) string {
	m := strings.ToLower(strings.TrimSpace(id))
	m = bracketRe.ReplaceAllString(m, "")
	for changed := true; changed; {
		changed = false
		for _, p := range providerPrefixes {
			if strings.HasPrefix(m, p) {
				m = strings.TrimPrefix(m, p)
				changed = true
			}
		}
	}
	m = atSuffixRe.ReplaceAllString(m, "")
	m = versionSuffixRe.ReplaceAllString(m, "")
	m = strings.TrimSuffix(m, "-latest")
	m = dateSuffixRe.ReplaceAllString(m, "")
	return m
}

// ModelFamily returns "opus", "sonnet", "haiku", "fable" or "mythos" when
// the (normalised) id names a Claude family, "" otherwise. Gemini ids never
// match.
func ModelFamily(normalized string) string {
	if strings.HasPrefix(normalized, "gemini-") || normalized == "" {
		return ""
	}
	for _, f := range []string{"opus", "sonnet", "haiku", "fable", "mythos"} {
		if strings.Contains(normalized, f) {
			return f
		}
	}
	return ""
}
