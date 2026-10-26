package translate

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func gifBytes(t *testing.T, w, h int, frames int) []byte {
	t.Helper()
	pal := color.Palette{color.Black, color.RGBA{255, 0, 0, 255}}
	g := &gif.GIF{}
	for i := 0; i < frames; i++ {
		img := image.NewPaletted(image.Rect(0, 0, w, h), pal)
		for x := 0; x < w; x++ {
			for y := 0; y < h; y++ {
				img.SetColorIndex(x, y, uint8(1-i%2)) // frame 0 red, frame 1 black
			}
		}
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 5)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAttachment_GIFIsSentAsAPNGOfItsFirstFrame(t *testing.T) {
	raw := gifBytes(t, 8, 6, 2)
	for _, label := range []string{"image/gif", "image/png", ""} { // a wrong label does not matter
		p := attach(t, "image", label, base64.StdEncoding.EncodeToString(raw))
		if gjson.Get(p, "inlineData.mimeType").String() != "image/png" {
			t.Fatalf("label %q: expected image/png inlineData, got %s", label, p)
		}
		dec, err := base64.StdEncoding.DecodeString(gjson.Get(p, "inlineData.data").String())
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(dec))
		if err != nil {
			t.Fatalf("the converted payload is not a PNG: %v", err)
		}
		if b := img.Bounds(); b.Dx() != 8 || b.Dy() != 6 {
			t.Fatalf("size = %v", b)
		}
		r, g, bl, _ := img.At(0, 0).RGBA()
		if r>>8 != 255 || g != 0 || bl != 0 {
			t.Fatalf("first frame (red) expected, got %v %v %v", r>>8, g, bl)
		}
	}
}

func TestGifFirstFrameToPNG_RefusesHugeCanvases(t *testing.T) {
	// A header that promises 60000x60000 pixels: must be refused before any decode allocation.
	hdr := []byte("GIF89a\x60\xea\x60\xea\x00\x00\x00;")
	if _, err := gifFirstFrameToPNG(hdr); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected a size refusal, got %v", err)
	}
	if _, err := gifFirstFrameToPNG([]byte("not a gif")); err == nil {
		t.Fatal("garbage must fail")
	}
}
