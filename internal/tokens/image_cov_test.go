package tokens

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"testing"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// webpHeader builds a 30-byte RIFF/WEBP header with the given chunk
// fourCC and 10-byte payload.
func webpHeader(fourCC string, payload [10]byte) []byte {
	h := []byte("RIFF\x1a\x00\x00\x00WEBP")
	h = append(h, fourCC...)
	h = append(h, 10, 0, 0, 0)
	return append(h, payload[:]...)
}

func vp8Payload(w, h int) [10]byte {
	return [10]byte{0, 0, 0, 0x9d, 0x01, 0x2a, byte(w), byte(w >> 8), byte(h), byte(h >> 8)}
}

func TestDecodeWebPDimensions(t *testing.T) {
	// VP8L packs (w-1) in 14 bits, (h-1) in the next 14.
	vp8l := func(w, h int) [10]byte {
		v := uint32(w-1) | uint32(h-1)<<14
		return [10]byte{0x2f, byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
	}
	vp8x := func(w, h int) [10]byte {
		w--
		h--
		return [10]byte{0, 0, 0, 0, byte(w), byte(w >> 8), byte(w >> 16), byte(h), byte(h >> 8), byte(h >> 16)}
	}
	bad := vp8Payload(10, 10)
	bad[4] = 0x02 // broken start code
	zero := vp8Payload(0, 5)
	noSig := vp8l(5, 5)
	noSig[0] = 0

	notRIFF := webpHeader("VP8 ", vp8Payload(1, 1))
	copy(notRIFF, "RIFX")
	notWebp := webpHeader("VP8 ", vp8Payload(1, 1))
	copy(notWebp[8:], "WEBX")

	cases := []struct {
		name   string
		data   []byte
		w, h   int
		wantOK bool
	}{
		{"lossy", webpHeader("VP8 ", vp8Payload(640, 480)), 640, 480, true},
		{"lossy masks 14 bits", webpHeader("VP8 ", [10]byte{0, 0, 0, 0x9d, 0x01, 0x2a, 0x10, 0xff, 0x20, 0xc0}), 0x3f10, 0x20, true},
		{"lossy zero width", webpHeader("VP8 ", zero), 0, 5, false},
		{"lossy bad start code", webpHeader("VP8 ", bad), 0, 0, false},
		{"lossless", webpHeader("VP8L", vp8l(1234, 567)), 1234, 567, true},
		{"lossless bad signature", webpHeader("VP8L", noSig), 0, 0, false},
		{"extended", webpHeader("VP8X", vp8x(4000, 3000)), 4000, 3000, true},
		{"unknown chunk", webpHeader("ABCD", vp8Payload(1, 1)), 0, 0, false},
		{"not RIFF", notRIFF, 0, 0, false},
		{"not WEBP", notWebp, 0, 0, false},
		{"too short", []byte("RIFF....WEBPVP8 "), 0, 0, false},
	}
	for _, c := range cases {
		w, h, ok := decodeWebPDimensions(b64(c.data))
		if ok != c.wantOK || (ok && (w != c.w || h != c.h)) {
			t.Errorf("%s: got (%d,%d,%v) want (%d,%d,%v)", c.name, w, h, ok, c.w, c.h, c.wantOK)
		}
	}
	if _, _, ok := decodeWebPDimensions("%%%not base64%%%"); ok {
		t.Error("garbage base64 must fail")
	}
}

func TestImageDimensions_AllFormats(t *testing.T) {
	var jb, gb bytes.Buffer
	if err := jpeg.Encode(&jb, image.NewRGBA(image.Rect(0, 0, 33, 21)), nil); err != nil {
		t.Fatal(err)
	}
	if err := gif.Encode(&gb, image.NewPaletted(image.Rect(0, 0, 17, 9), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data string
		w, h int
		ok   bool
	}{
		{"png", pngB64(t, 40, 30), 40, 30, true},
		{"jpeg", b64(jb.Bytes()), 33, 21, true},
		{"gif", b64(gb.Bytes()), 17, 9, true},
		{"webp", b64(webpHeader("VP8 ", vp8Payload(321, 123))), 321, 123, true},
		{"empty", "", 0, 0, false},
		{"garbage", b64([]byte("definitely not an image at all, just text")), 0, 0, false},
	}
	for _, c := range cases {
		w, h, ok := imageDimensions(c.data)
		if ok != c.ok || w != c.w || h != c.h {
			t.Errorf("%s: got (%d,%d,%v) want (%d,%d,%v)", c.name, w, h, ok, c.w, c.h, c.ok)
		}
	}
}

func TestEstimateImageTokensP_Gemini2Tiling(t *testing.T) {
	cases := []struct {
		name string
		data string
		want int
	}{
		{"single tile", pngB64(t, 100, 100), 258},
		{"two tiles wide", pngB64(t, 1000, 500), 2 * 258},
		{"portrait mirror", pngB64(t, 500, 1000), 2 * 258},
		{"downscaled to 2x2", pngB64(t, 4000, 4000), 4 * 258},
		{"max 4x2", pngB64(t, 3072, 1536), 8 * 258},
		{"extreme aspect is scaled", b64(webpHeader("VP8X", [10]byte{0, 0, 0, 0, 0x1f, 0x4e, 0, 99, 0, 0})), 4 * 258},
		{"unreadable falls back to floor", "AAAA", anthropicImageTokenFloor},
	}
	for _, c := range cases {
		if got := estimateImageTokensP(c.data, Gemini2Profile); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
	if got := estimateImageTokens(pngB64(t, 100, 100)); got != 258 {
		t.Errorf("estimateImageTokens default = %d", got)
	}
}

func TestEstimateImageTokensP_AreaPricing(t *testing.T) {
	if got := estimateImageTokensP(pngB64(t, 100, 100), Gemini3Profile); got != Gemini3Profile.ImageMinTokens {
		t.Errorf("small image should floor at min: %d", got)
	}
	if got := estimateImageTokensP(pngB64(t, 4000, 4000), Gemini3Profile); got != Gemini3Profile.ImageMaxTokens {
		t.Errorf("large image should cap at max: %d", got)
	}
	if got := estimateImageTokensP("AAAA", Gemini3Profile); got != Gemini3Profile.ImageMaxTokens {
		t.Errorf("unreadable should price at max: %d", got)
	}
	// 1000x1000 = 1_000_000 px / 750 -> 1334, capped at 1120.
	custom := Profile{ImagePixelsPerToken: 1000, ImageMaxTokens: 5000, ImageMinTokens: 10}
	if got := estimateImageTokensP(pngB64(t, 100, 100), custom); got != 10 {
		t.Errorf("ceil(10000/1000)=10: %d", got)
	}
	if got := estimateImageTokensP(pngB64(t, 101, 100), custom); got != 11 {
		t.Errorf("ceil division: %d", got)
	}
	zeroMax := Profile{ImagePixelsPerToken: 100, ImageMaxTokens: 0}
	if got := estimateImageTokensP(pngB64(t, 10, 10), zeroMax); got != 1 {
		t.Errorf("never below 1: %d", got)
	}
}

// 1053x4210 scales to a height of 3072.0000000000005 under float math,
// which rounds up to 5 tiles (2x5=10 > 8) and must be re-fit to the
// 8-tile ceiling.
func TestEstimateImageTokensP_FloatOvershootIsRefitToEightTiles(t *testing.T) {
	x := func(w, h int) [10]byte {
		w--
		h--
		return [10]byte{0, 0, 0, 0, byte(w), byte(w >> 8), byte(w >> 16), byte(h), byte(h >> 8), byte(h >> 16)}
	}
	got := estimateImageTokensP(b64(webpHeader("VP8X", x(1053, 4210))), Gemini2Profile)
	if got != 4*geminiImageTileTokens {
		t.Fatalf("tokens = %d, want 4 tiles after the 8-tile re-fit", got)
	}
}
