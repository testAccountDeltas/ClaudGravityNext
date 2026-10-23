// Part of the tokens package — see text.go for the package doc comment.
// Direct port of v1's image-dimension-based pricing (token_estimator.go),
// shared by both estimators in v1 already (single owner even before this
// merge).
package tokens

import (
	"encoding/base64"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"strings"

	"claudgravity/internal/b64util"
)

// anthropicImageTokenFloor is the flat fallback used when the actual pixel
// dimensions can't be determined.
const anthropicImageTokenFloor = 1600

// GAP-D4-09: Anthropic's own image-pricing formula below (ceil(w*h/750),
// floor 1600) is what Claude's REAL API bills — but this gateway never
// talks to Claude's real API, it talks to Gemini, whose actual per-image
// cost is much lower (flat 258 tokens for an image that fits in one
// tile, ≤384px either side; larger images tile at 768px with each tile
// still only 258 tokens). Pricing every image at Anthropic's formula
// systematically overcounts on this backend, triggering premature
// truncation of valuable image-heavy conversation history well before
// the context window Gemini actually enforces is anywhere near full.
const (
	geminiImageTileSize   = 768
	geminiImageTileTokens = 258
)

// estimateImageTokens computes Gemini's real native image-pricing
// tiling rule (see the GAP-D4-09 comment above) by decoding only the
// image header (no full pixel decode) via image.DecodeConfig fed a
// streaming base64 decoder. Falls back to Anthropic's own floor only
// when dimensions genuinely can't be read — a few extra tokens of
// safety margin there is far cheaper than guessing low.
func estimateImageTokens(b64Data string) int {
	if b64Data == "" {
		return anthropicImageTokenFloor
	}
	cleaned := b64util.NormalizeBase64(b64Data)
	cfg, _, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(cleaned)))
	var width, height int
	if err == nil && cfg.Width > 0 && cfg.Height > 0 {
		width, height = cfg.Width, cfg.Height
	} else {
		// Go's stdlib image package has no built-in WebP decoder, so
		// image.DecodeConfig always fails on a WebP image — parse just the
		// 30-byte RIFF/WebP header by hand instead.
		if w, h, ok := decodeWebPDimensions(cleaned); ok {
			width, height = w, h
		} else {
			return anthropicImageTokenFloor
		}
	}
	wFloat, hFloat := float64(width), float64(height)
	longest, shortest := wFloat, hFloat
	if shortest > longest {
		longest, shortest = shortest, longest
	}
	if longest > 3072 || shortest > 1536 {
		scale := 3072.0 / longest
		if scaleShort := 1536.0 / shortest; scaleShort < scale {
			scale = scaleShort
		}
		wFloat *= scale
		hFloat *= scale
	}
	tilesX := int(math.Ceil(wFloat / geminiImageTileSize))
	tilesY := int(math.Ceil(hFloat / geminiImageTileSize))
	if tilesX < 1 {
		tilesX = 1
	}
	if tilesY < 1 {
		tilesY = 1
	}
	if tilesX*tilesY > 8 {
		scale := math.Sqrt(8.0 / float64(tilesX*tilesY))
		wFloat *= scale
		hFloat *= scale
		tilesX = int(math.Ceil(wFloat / geminiImageTileSize))
		tilesY = int(math.Ceil(hFloat / geminiImageTileSize))
		if tilesX < 1 {
			tilesX = 1
		}
		if tilesY < 1 {
			tilesY = 1
		}
		for tilesX*tilesY > 8 {
			if tilesX >= tilesY && tilesX > 1 {
				tilesX--
			} else if tilesY > 1 {
				tilesY--
			} else {
				break
			}
		}
	}
	return tilesX * tilesY * geminiImageTileTokens
}

// decodeWebPDimensions hand-parses just enough of a WebP file's RIFF
// container to read its pixel dimensions, covering all three real payload
// shapes (simple lossy VP8, lossless VP8L, and the extended VP8X
// container). Decodes only the first 30 bytes of base64.
func decodeWebPDimensions(b64Data string) (width, height int, ok bool) {
	buf := make([]byte, 30)
	n, _ := io.ReadFull(base64.NewDecoder(base64.StdEncoding, strings.NewReader(b64Data)), buf)
	if n < 30 {
		return 0, 0, false
	}
	if string(buf[0:4]) != "RIFF" || string(buf[8:12]) != "WEBP" {
		return 0, 0, false
	}
	fourCC := string(buf[12:16])
	payload := buf[20:]
	switch fourCC {
	case "VP8 ": // simple lossy: 3-byte frame tag, 3-byte start code, then 14-bit W/H
		if len(payload) < 10 || payload[3] != 0x9d || payload[4] != 0x01 || payload[5] != 0x2a {
			return 0, 0, false
		}
		w := int(payload[6]) | int(payload[7]&0x3f)<<8
		h := int(payload[8]) | int(payload[9]&0x3f)<<8
		return w, h, w > 0 && h > 0
	case "VP8L": // lossless: 1-byte signature 0x2f, then a packed 32-bit LE field
		if len(payload) < 5 || payload[0] != 0x2f {
			return 0, 0, false
		}
		val := uint32(payload[1]) | uint32(payload[2])<<8 | uint32(payload[3])<<16 | uint32(payload[4])<<24
		w := int(val&0x3FFF) + 1
		h := int((val>>14)&0x3FFF) + 1
		return w, h, true
	case "VP8X": // extended container: 1 flags byte, 3 reserved, then 24-bit W-1/H-1
		if len(payload) < 10 {
			return 0, 0, false
		}
		w := int(payload[4]) | int(payload[5])<<8 | int(payload[6])<<16
		h := int(payload[7]) | int(payload[8])<<8 | int(payload[9])<<16
		return w + 1, h + 1, true
	}
	return 0, 0, false
}
