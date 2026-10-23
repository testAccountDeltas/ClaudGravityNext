// Part of the tokens package — see text.go for the package doc comment.
// Direct port of v1's PDF page-count heuristic. minPDFTokenFloor lived
// only in context_window.go in v1 (token_estimator.go called it by
// reference, same package) — kept here as the one copy, consistent with
// the rest of this merge.
package tokens

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"io"
	"regexp"
	"strconv"

	"claudgravity/internal/b64util"
)

// minPDFTokenFloor is the minimum token estimate ever returned for a PDF
// (or unrecognized-media_type, defaulted-to-PDF) base64 document.
const minPDFTokenFloor = 2500

// geminiPDFTokensPerPage is Gemini's real documented flat per-page PDF
// billing, independent of how text-dense or image-dense that page is.
const geminiPDFTokensPerPage = 258

// pdfPageCountPattern looks for an uncompressed PDF page-tree's /Count key
// directly in the raw decoded bytes — a best-effort heuristic, not a real
// PDF parser. Anchored to "/Type /Pages" specifically so it doesn't match
// an unrelated "/Type /Outlines /Count N" (a document outline's own entry
// count, commonly much smaller than the real page count).
var (
	typePagesPattern            = regexp.MustCompile(`/Type\s*/Pages\b`)
	countPattern                = regexp.MustCompile(`/Count\s+(\d+)`)
	pdfDictPattern              = regexp.MustCompile(`<<[^\r\n]*?/Type\s*/Pages[\s\S]*?>>`)
	pdfPageCountPattern         = regexp.MustCompile(`/Type\s*/Pages[^>]*?/Count\s+(\d+)`)
	pdfPageCountPatternReversed = regexp.MustCompile(`/Count\s+(\d+)[^>]*?/Type\s*/Pages`)
)

// extractBalancedPDFPagesCounts scans raw bytes for << ... >> dictionaries
// with balanced angle brackets that contain /Type /Pages, and extracts their
// /Count values. Handling balanced brackets allows nested sub-dictionaries
// (e.g. /Resources << /Font ... >>) before /Count without premature stopping.
func extractBalancedPDFPagesCounts(raw []byte) []int {
	var counts []int
	n := len(raw)
	i := 0
	for i < n-1 {
		if raw[i] == '<' && raw[i+1] == '<' {
			start := i
			depth := 1
			i += 2
			for i < n-1 && depth > 0 {
				if raw[i] == '<' && raw[i+1] == '<' {
					depth++
					i += 2
				} else if raw[i] == '>' && raw[i+1] == '>' {
					depth--
					i += 2
				} else {
					i++
				}
			}
			if depth == 0 {
				dict := raw[start:i]
				if typePagesPattern.Match(dict) {
					for _, cm := range countPattern.FindAllSubmatch(dict, -1) {
						if p, err := strconv.Atoi(string(cm[1])); err == nil && p > 0 {
							counts = append(counts, p)
						}
					}
				}
			}
		} else {
			i++
		}
	}
	return counts
}

// estimatePDFTokensFromPageCount decodes base64 PDF data and looks for a
// plaintext /Count N. Returns (0, false) if decoding fails or no page
// count is found, letting the caller fall back to its own density-based
// estimate.
func estimatePDFTokensFromPageCount(b64Data string) (int, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64util.NormalizeBase64(b64Data))
	if err != nil || len(raw) == 0 {
		return 0, false
	}
	counts := extractBalancedPDFPagesCounts(raw)
	if len(counts) == 0 {
		for _, m := range pdfDictPattern.FindAll(raw, -1) {
			for _, cm := range countPattern.FindAllSubmatch(m, -1) {
				if p, err := strconv.Atoi(string(cm[1])); err == nil && p > 0 {
					counts = append(counts, p)
				}
			}
		}
		for _, m := range pdfPageCountPattern.FindAllSubmatch(raw, -1) {
			if p, err := strconv.Atoi(string(m[1])); err == nil && p > 0 {
				counts = append(counts, p)
			}
		}
		for _, m := range pdfPageCountPatternReversed.FindAllSubmatch(raw, -1) {
			if p, err := strconv.Atoi(string(m[1])); err == nil && p > 0 {
				counts = append(counts, p)
			}
		}
	}
	if len(counts) == 0 {
		counts = extractPagesFromDecompressedStreams(raw)
	}
	if len(counts) == 0 {
		return 0, false
	}
	// A nested/intermediate /Pages node carries its own smaller subtree
	// /Count — take the largest match found, always the root's total.
	maxPages := 0
	for _, pages := range counts {
		if pages > maxPages {
			maxPages = pages
		}
	}
	if maxPages == 0 {
		return 0, false
	}
	return maxPages * geminiPDFTokensPerPage, true
}

// extractPagesFromDecompressedStreams scans raw bytes for Flate-compressed
// object streams (/ObjStm) in PDF 1.5+ documents, decompressing up to 50 streams
// to extract /Count values for page trees that are compressed.
func extractPagesFromDecompressedStreams(raw []byte) []int {
	var counts []int
	streamMarker := []byte("stream")
	endstreamMarker := []byte("endstream")
	n := len(raw)
	pos := 0
	streamsChecked := 0

	for pos < n && streamsChecked < 50 {
		idx := bytes.Index(raw[pos:], streamMarker)
		if idx == -1 {
			break
		}
		streamStart := pos + idx + len(streamMarker)
		if streamStart < n && raw[streamStart] == '\r' {
			streamStart++
		}
		if streamStart < n && raw[streamStart] == '\n' {
			streamStart++
		}

		endIdx := bytes.Index(raw[streamStart:], endstreamMarker)
		if endIdx == -1 {
			break
		}
		streamEnd := streamStart + endIdx
		for streamEnd > streamStart && (raw[streamEnd-1] == '\r' || raw[streamEnd-1] == '\n') {
			streamEnd--
		}

		streamBytes := raw[streamStart:streamEnd]
		pos = streamStart + endIdx + len(endstreamMarker)
		streamsChecked++

		if len(streamBytes) > 10 {
			zr, err := zlib.NewReader(bytes.NewReader(streamBytes))
			if err == nil {
				lr := io.LimitReader(zr, 256*1024)
				decompressed, readErr := io.ReadAll(lr)
				_ = zr.Close()
				if readErr == nil && len(decompressed) > 0 {
					if typePagesPattern.Match(decompressed) {
						for _, cm := range countPattern.FindAllSubmatch(decompressed, -1) {
							if p, err := strconv.Atoi(string(cm[1])); err == nil && p > 0 {
								counts = append(counts, p)
							}
						}
					}
				}
			}
		}
	}
	return counts
}
