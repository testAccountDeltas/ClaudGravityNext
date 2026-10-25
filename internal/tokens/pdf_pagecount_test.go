package tokens

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"strings"
	"testing"
)

func b64pdf(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func deflate(t *testing.T, s string) string {
	t.Helper()
	var b bytes.Buffer
	zw := zlib.NewWriter(&b)
	// Padding makes the encoder emit a real Huffman block; a tiny input is
	// stored verbatim, which would let the plain-text scan find the tree and
	// defeat the point of these cases.
	if _, err := zw.Write([]byte(strings.Repeat("0 0 m 1 1 l S ", 40) + s)); err != nil {
		t.Fatal(err)
	}
	_ = zw.Close()
	if bytes.Contains(b.Bytes(), []byte("/Type")) {
		t.Fatal("test data is not actually compressed")
	}
	return b.String()
}

func pagesTok(n int) int { return n * Gemini2Profile.PDFTokensPerPage }

func TestPDFPageCount_Layouts(t *testing.T) {
	for _, c := range []struct {
		name string
		pdf  func(t *testing.T) string
		want int // 0 = no page count recoverable
	}{
		{"plain page tree", func(*testing.T) string {
			return "%PDF-1.4\n1 0 obj\n<< /Type /Pages /Kids [2 0 R] /Count 12 >>\nendobj"
		}, pagesTok(12)},
		{"nested dictionary before /Count", func(*testing.T) string {
			return "<< /Type /Pages /Resources << /Font << /F1 5 0 R >> >> /Count 9 >>"
		}, pagesTok(9)},
		{"the root total beats a subtree's", func(*testing.T) string {
			return "<< /Type /Pages /Count 3 /Parent 1 0 R >> << /Type /Pages /Count 40 >>"
		}, pagesTok(40)},
		{"the outline's /Count is not the page count", func(*testing.T) string {
			return "<< /Type /Outlines /Count 99 >> << /Type /Pages /Count 4 >>"
		}, pagesTok(4)},
		{"count before type", func(*testing.T) string { return "<< /Count 6 /Type /Pages >>" }, pagesTok(6)},
		{"unbalanced dictionary falls back to the regexes", func(*testing.T) string { return "<< /Type /Pages /Count 5 /Kids [" }, pagesTok(5)},
		{"compressed object stream", func(t *testing.T) string {
			return "%PDF-1.5\n7 0 obj\n<< /Type /ObjStm >>\nstream\n" + deflate(t, "<< /Type /Pages /Count 31 /Kids [] >>") + "\nendstream\nendobj"
		}, pagesTok(31)},
		{"compressed stream with CRLF framing", func(t *testing.T) string {
			return "x stream\r\n" + deflate(t, "<< /Type /Pages /Count 8 >>") + "\r\nendstream"
		}, pagesTok(8)},
		{"compressed stream without a page tree", func(t *testing.T) string {
			return "stream\n" + deflate(t, "just some page content, no tree here at all") + "\nendstream"
		}, 0},
		{"stream that is not zlib", func(*testing.T) string { return "stream\nthis is not compressed data at all\nendstream" }, 0},
		{"stream marker without an end", func(*testing.T) string { return "stream\nabcdefghijklmnop" }, 0},
		{"no page tree at all", func(*testing.T) string { return "%PDF-1.4 hello" }, 0},
		{"zero is not a count", func(*testing.T) string { return "<< /Type /Pages /Count 0 >>" }, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := estimatePDFTokensFromPageCount(b64pdf(c.pdf(t)))
			if c.want == 0 {
				if ok {
					t.Fatalf("no count expected, got %d", got)
				}
				return
			}
			if !ok || got != c.want {
				t.Fatalf("got (%d,%v), want %d", got, ok, c.want)
			}
		})
	}
}

func TestPDFPageCount_BadInput(t *testing.T) {
	for _, in := range []string{"", "!!!not base64!!!"} {
		if got, ok := estimatePDFTokensFromPageCount(in); ok || got != 0 {
			t.Errorf("%q -> (%d,%v), want (0,false)", in, got, ok)
		}
	}
}

// Only the first 50 streams are inflated, so a page tree hidden behind a
// pile of junk streams is not found (bounded work on hostile input).
func TestPDFPageCount_StreamScanIsBounded(t *testing.T) {
	junk := strings.Repeat("stream\nxxxxxxxxxxxx\nendstream\n", 50)
	pdf := junk + "stream\n" + deflate(t, "<< /Type /Pages /Count 77 >>") + "\nendstream"
	if got, ok := estimatePDFTokensFromPageCount(b64pdf(pdf)); ok {
		t.Fatalf("stream #51 must not be inspected, got %d", got)
	}
}

func TestPDFPageCount_DecompressionIsCapped(t *testing.T) {
	// The tree sits after 300 KiB of padding: beyond the 256 KiB inflate cap.
	big := strings.Repeat("A", 300*1024) + "<< /Type /Pages /Count 5 >>"
	pdf := "stream\n" + deflate(t, big) + "\nendstream"
	if got, ok := estimatePDFTokensFromPageCount(b64pdf(pdf)); ok {
		t.Fatalf("data past the inflate cap must not be read, got %d", got)
	}
}
