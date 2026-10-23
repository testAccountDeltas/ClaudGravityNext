package tokens

import "strings"

// IsSVGMediaType checks if the media type represents an SVG image in any standard or common variant.
func IsSVGMediaType(mt string) bool {
	return isSVGMediaType(mt)
}

func isSVGMediaType(mt string) bool {
	mt = strings.ToLower(strings.TrimSpace(mt))
	if idx := strings.Index(mt, ";"); idx != -1 {
		mt = strings.TrimSpace(mt[:idx])
	}
	return mt == "image/svg+xml" || mt == "image/svg" || mt == "image/x-svg" || mt == "image/svg-xml"
}
