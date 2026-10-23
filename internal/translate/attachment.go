// Part of the translate package — see images.go for the attachment paths
// that call into this. Decides, for one decoded attachment, whether it can
// go to Gemini as inlineData, as plain text, or only as a short visible
// placeholder. Nothing is ever forwarded as garbage and nothing is dropped
// silently: an attachment Gemini cannot take is replaced by a placeholder
// that names its type and size.
package translate

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/sjson"

	"claudgravity/internal/b64util"
)

// geminiImageTypes are the image MIME types Gemini accepts as inlineData
// (GIF, BMP, TIFF and friends are NOT supported).
var geminiImageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/webp": true,
	"image/heic": true, "image/heif": true,
}

// sniffMIME extends http.DetectContentType with the HEIC/HEIF brands it
// does not know.
func sniffMIME(b []byte) string {
	if len(b) >= 12 && string(b[4:8]) == "ftyp" {
		switch string(b[8:12]) {
		case "heic", "heix", "hevc", "hevx":
			return "image/heic"
		case "mif1", "msf1", "heim", "heis":
			return "image/heif"
		}
	}
	return strings.ToLower(strings.TrimSpace(strings.SplitN(http.DetectContentType(b), ";", 2)[0]))
}

// isTextualMIME reports media types whose bytes are meant to be read as text.
func isTextualMIME(mt string) bool {
	if strings.HasPrefix(mt, "text/") || strings.HasSuffix(mt, "+json") || strings.HasSuffix(mt, "+xml") {
		return true
	}
	switch mt {
	case "application/json", "application/xml", "application/x-yaml", "application/yaml",
		"application/javascript", "application/x-ndjson", "application/csv", "application/markdown":
		return true
	}
	return false
}

func omitted(kind, why string) string {
	return "[" + kind + " omitted: " + why + "]"
}

// classifyAttachment turns raw attachment bytes into exactly one outcome:
// a ready inlineData part (binary==true), or text (binary==false) that is
// either the decoded document or a placeholder. b64 may carry the already
// normalized base64 of raw (avoids re-encoding); pass "" to encode lazily.
func classifyAttachment(ptype, mediaType, name string, raw []byte, b64 string) (part string, binary bool, text string) {
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	if i := strings.Index(mt, ";"); i != -1 {
		mt = strings.TrimSpace(mt[:i])
	}
	if mt == "" {
		mt = "image/png"
		if ptype == "document" {
			mt = "application/pdf"
		}
	}
	if mt == "image/jpg" {
		mt = "image/jpeg"
	}
	kind := "document"
	if strings.HasPrefix(mt, "image/") {
		kind = "image"
	}
	describe := mt
	if name != "" {
		describe += ", name \"" + name + "\""
	}
	describe += fmt.Sprintf(", %d bytes", len(raw))
	if len(raw) == 0 {
		return "", false, omitted(kind, "empty payload ("+mt+")")
	}

	switch {
	case isSVGMediaType(mt) || isTextualMIME(mt):
		if !utf8.Valid(raw) {
			return "", false, omitted(kind, "not valid UTF-8 text ("+describe+")")
		}
		return "", false, string(raw)
	case kind == "image":
		if !geminiImageTypes[mt] {
			return "", false, omitted("image", "unsupported type "+mt)
		}
		sniffed := sniffMIME(raw)
		if !geminiImageTypes[sniffed] {
			return "", false, omitted("image", "data is not a valid "+mt+" image")
		}
		mt = sniffed // trust the bytes over a wrong label
	case mt == "application/pdf":
		head := raw
		if len(head) > 1024 {
			head = head[:1024]
		}
		if !strings.Contains(string(head), "%PDF-") {
			return "", false, omitted("document", "data is not a valid PDF ("+describe+")")
		}
	default:
		return "", false, omitted("document", "unsupported type "+describe)
	}
	if b64 == "" {
		b64 = base64.StdEncoding.EncodeToString(raw)
	}
	p := `{"inlineData":{"mimeType":"","data":""}}`
	p, _ = sjson.Set(p, "inlineData.mimeType", mt)
	p, _ = sjson.Set(p, "inlineData.data", b64)
	return p, true, ""
}

var extMIME = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".heic": "image/heic", ".heif": "image/heif", ".bmp": "image/bmp",
	".svg": "image/svg+xml", ".pdf": "application/pdf", ".txt": "text/plain", ".md": "text/markdown",
	".json": "application/json", ".csv": "text/csv", ".xml": "application/xml", ".html": "text/html",
	".htm": "text/html", ".docx": "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", ".zip": "application/zip",
}

func isOpaqueMIME(mt string) bool {
	switch mt {
	case "", "application/octet-stream", "binary/octet-stream", "application/binary", "application/x-unknown", "application/unknown":
		return true
	}
	return false
}

// resolveFetchedMIME picks the media type of a fetched URL body. A
// meaningful Content-Type wins; an empty/octet-stream one is replaced by
// content sniffing, then the URL's file extension.
func resolveFetchedMIME(contentType, rawURL string, body []byte) string {
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
	if !isOpaqueMIME(mt) {
		return mt
	}
	sniffed := sniffMIME(body)
	if sniffed != "" && sniffed != "application/octet-stream" && sniffed != "text/plain" {
		return sniffed
	}
	if u, err := url.Parse(rawURL); err == nil {
		if m, ok := extMIME[strings.ToLower(path.Ext(u.Path))]; ok {
			return m
		}
	}
	if sniffed == "text/plain" {
		return sniffed
	}
	return "application/octet-stream"
}

// decodeAttachmentSource resolves an image/document source's base64
// payload into a ready inlineData part (isBinary==true) or text
// (isBinary==false): the decoded document, or a visible placeholder when
// the payload is not valid base64, is not what its media_type claims, or is
// a type Gemini cannot take (see classifyAttachment). ok==false only when
// there is no payload at all.
func decodeAttachmentSource(ptype, rawMediaType, rawData, name string) (inlineDataPart string, isBinary bool, decodedText string, ok bool) {
	data := b64util.NormalizeBase64(rawData)
	if data == "" {
		return "", false, "", false
	}
	raw, err := decodeBase64Loose(data)
	if err != nil {
		kind := ptype
		if kind == "" {
			kind = "attachment"
		}
		return "", false, omitted(kind, "payload is not valid base64"), true
	}
	part, bin, text := classifyAttachment(ptype, rawMediaType, name, raw, data)
	return part, bin, text, true
}

// fetchAttachmentFromURL fetches a url-sourced attachment (see urlfetch.go
// for the SSRF-safe transport) and routes it through the same
// classification as a base64 source. The media type comes from the
// Content-Type header when it is meaningful, else from content sniffing,
// else from the URL's extension.
func fetchAttachmentFromURL(ptype, rawURL, name string) (inlineDataPart string, isBinary bool, text string, ok bool) {
	body, contentType, fetched := fetchURLAttachment(rawURL)
	if !fetched {
		return "", false, "", false
	}
	if len(body) == 0 {
		return "", false, omitted(ptype, "empty response from URL"), true
	}
	part, bin, txt := classifyAttachment(ptype, resolveFetchedMIME(contentType, rawURL, body), name, body, "")
	return part, bin, txt, true
}
