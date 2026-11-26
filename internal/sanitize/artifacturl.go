package sanitize

import "strings"

var artifactURLPrefixes = [...]string{
	"https://claude.ai/code/artifact/",
	"https://claude.ai/artifact/",
	"http://claude.ai/code/artifact/",
	"http://claude.ai/artifact/",
}

const maxArtifactURLPrefixLen = len("https://claude.ai/code/artifact/")

// RewriteArtifactURLs rewrites Claude artifact viewer URLs in model output to
// baseURL. Fast-paths immediately with zero allocations unless "claude.ai/" is
// present in text.
func RewriteArtifactURLs(text, baseURL string) string {
	if baseURL == "" || !strings.Contains(text, "claude.ai/") {
		return text
	}
	base := strings.TrimRight(baseURL, "/")
	for _, p := range artifactURLPrefixes {
		if strings.Contains(text, p) {
			suffix := "/artifact/"
			if strings.HasSuffix(p, "/code/artifact/") {
				suffix = "/code/artifact/"
			}
			text = strings.ReplaceAll(text, p, base+suffix)
		}
	}
	return text
}

// PartialArtifactURLSuffix returns any trailing substring at the end of s that
// matches a non-empty proper prefix of a Claude artifact URL (for chunked SSE
// hold-back), or "" when no hold-back is needed.
func PartialArtifactURLSuffix(s string) string {
	if s == "" {
		return ""
	}
	tail := s
	if len(tail) > maxArtifactURLPrefixLen-1 {
		tail = tail[len(tail)-(maxArtifactURLPrefixLen-1):]
	}
	for i := 0; i < len(tail); i++ {
		if tail[i] != 'h' {
			continue
		}
		cand := tail[i:]
		for _, p := range artifactURLPrefixes {
			if len(cand) < len(p) && strings.HasPrefix(p, cand) {
				return cand
			}
		}
	}
	return ""
}
