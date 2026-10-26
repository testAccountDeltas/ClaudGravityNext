// Part of the translate package — see attachment.go for the classification
// that calls it. Gemini accepts PNG, JPEG, WEBP, HEIC and HEIF as inlineData
// but not GIF, so a GIF attachment is converted to a PNG of its first frame
// (the standard library decodes GIF and encodes PNG; nothing is dropped
// silently and the model still sees the picture).
package translate

import (
	"bytes"
	"fmt"
	"image/gif"
	"image/png"
)

// maxGIFPixels bounds the decode: a GIF header can promise a canvas far larger
// than its file size, and decoding allocates width*height.
const maxGIFPixels = 25_000_000

// gifFirstFrameToPNG returns the first frame of a GIF as PNG bytes.
func gifFirstFrameToPNG(raw []byte) ([]byte, error) {
	cfg, err := gif.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxGIFPixels {
		return nil, fmt.Errorf("%dx%d is too large to convert", cfg.Width, cfg.Height)
	}
	img, err := gif.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
