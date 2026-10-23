// Part of the tokens package — see text.go for the package doc comment.
// Per-model-family media pricing. Gemini 2.x bills a flat 258 tokens per
// 768px image tile and per PDF page; Gemini 3.x bills images at the
// default (high) media resolution by pixel area, capped at 1120 tokens,
// and PDF pages at 560 (Audit v2, item P.17).
package tokens

import "strings"

// Profile holds the media-pricing constants of one Gemini model family.
type Profile struct {
	Name string
	// PDFTokensPerPage prices one PDF page.
	PDFTokensPerPage int
	// ImagePixelsPerToken > 0 selects area pricing (Gemini 3.x):
	// ceil(w*h/ImagePixelsPerToken) capped at ImageMaxTokens, which is
	// also the price when the dimensions cannot be read. 0 selects the
	// 768px tile pricing of Gemini 2.x.
	ImagePixelsPerToken int
	ImageMaxTokens      int
	// ImageMinTokens floors area pricing: live Gemini 3.8 measurements
	// show a flat ~1088 tokens for 32px..1024px images.
	ImageMinTokens int
}

// Gemini2Profile is the legacy 258-per-tile / 258-per-page pricing.
var Gemini2Profile = Profile{Name: "gemini-2.x", PDFTokensPerPage: geminiPDFTokensPerPage}

// Gemini3Profile prices at the default media resolution: ~1120 tokens for
// an image (measured live: 32x32 .. 1024x1024 all bill ~1088), 560 per PDF page.
var Gemini3Profile = Profile{
	Name:                "gemini-3.x",
	PDFTokensPerPage:    560,
	ImagePixelsPerToken: 750,
	ImageMaxTokens:      1120,
	ImageMinTokens:      1088,
}

// defaultProfile is used by callers that do not know the model: this
// gateway maps everything onto Gemini 3.x.
var defaultProfile = Gemini3Profile

// ProfileForModel selects the pricing family for a resolved Gemini model
// name; only an explicit gemini-2* name gets the legacy constants.
func ProfileForModel(model string) Profile {
	m := strings.ToLower(strings.TrimSpace(model))
	if strings.HasPrefix(m, "gemini-2") || strings.HasPrefix(m, "models/gemini-2") {
		return Gemini2Profile
	}
	return Gemini3Profile
}
