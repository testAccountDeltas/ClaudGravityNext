// Part of the translate package — see translate.go for the package doc
// comment. Direct port of the "image"/"document" content-block handling
// inside v1's translator.go Step 5 — both the top-level message-content
// path (convertImageDocumentBlock) and the nested tool_result-content
// path (appendAttachmentToToolResult, used by toolresult.go), which v1
// duplicates as two separate switches over the same source shapes.
package translate

import (
	"encoding/base64"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/b64util"
	"claudgravity/internal/tokens"
)

// decodeBase64Loose decodes a base64 string that may contain embedded
// whitespace (RFC 2045 line-wrapping at 76 chars, or stray \r\n/\n/spaces
// some clients leave in). base64.StdEncoding.DecodeString rejects any
// non-alphabet byte outright with CorruptInputError, silently dropping an
// otherwise well-formed document whose encoder happened to wrap lines.
func decodeBase64Loose(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(b64util.NormalizeBase64(s))
}

func isSVGMediaType(mediaType string) bool {
	return tokens.IsSVGMediaType(mediaType)
}

// decodeAttachmentSource resolves an image/document source's base64
// payload into either a ready inlineData part (isBinary==true) or
// decoded plain text (isBinary==false) — the media_type defaulting,
// data-URI stripping, and SVG-as-text exception both the top-level and
// nested tool_result attachment paths need.
func decodeAttachmentSource(ptype, rawMediaType, rawData string) (inlineDataPart string, isBinary bool, decodedText string, ok bool) {
	mediaType := strings.ToLower(rawMediaType)
	if idx := strings.Index(mediaType, ";"); idx != -1 {
		mediaType = strings.TrimSpace(mediaType[:idx])
	}
	data := b64util.NormalizeBase64(rawData)
	if data == "" {
		return "", false, "", false
	}
	if mediaType == "" {
		if ptype == "document" {
			mediaType = "application/pdf"
		} else {
			mediaType = "image/png"
		}
	}
	if !isSVGMediaType(mediaType) && (mediaType == "application/pdf" || strings.HasPrefix(mediaType, "image/")) {
		part := `{"inlineData":{"mimeType":"","data":""}}`
		part, _ = sjson.Set(part, "inlineData.mimeType", mediaType)
		part, _ = sjson.Set(part, "inlineData.data", data)
		return part, true, "", true
	}
	decoded, err := decodeBase64Loose(data)
	if err != nil || len(decoded) == 0 {
		return "", false, "", false
	}
	return "", false, string(decoded), true
}

// fetchAttachmentFromURL fetches a url-sourced attachment (see urlfetch.go
// for the SSRF-safe transport) and routes it through the exact same
// media-type decision decodeAttachmentSource already makes for a base64
// source, so a fetched PDF/image gets inlineData and a fetched text
// document gets a plain text part, consistently with every other
// attachment path in this file.
func fetchAttachmentFromURL(ptype, rawURL string) (inlineDataPart string, isBinary bool, text string, ok bool) {
	body, contentType, fetched := fetchURLAttachment(rawURL)
	if !fetched {
		return "", false, "", false
	}
	mediaType := contentType
	if idx := strings.Index(mediaType, ";"); idx != -1 {
		mediaType = mediaType[:idx]
	}
	mediaType = strings.TrimSpace(mediaType)
	return decodeAttachmentSource(ptype, mediaType, base64.StdEncoding.EncodeToString(body))
}

// convertImageDocumentBlock converts one top-level "image"/"document"
// message-content block into zero or more Gemini parts.
func convertImageDocumentBlock(ptype string, p gjson.Result, role string) []string {
	var parts []string

	if role == "assistant" {
		// Gemini only allows text/thought/functionCall parts in a
		// "model"-role turn — inlineData there (an imported conversation
		// history, or a multimodal subagent's own prior turn) gets
		// rejected outright with "Invalid part type for model role".
		// Swap it for a text placeholder instead of silently producing
		// an upstream 400 — but only for genuinely BINARY attachments: a
		// text document (source.type=="text"/"content", or a base64
		// text-ish media_type) decodes to a plain "text" part below,
		// which Gemini allows fine in the model role.
		srcTypePeek := p.Get("source.type").String()
		if srcTypePeek == "url" {
			part := `{"text":""}`
			part, _ = sjson.Set(part, "text", "[Assistant output "+ptype+": url]")
			return append(parts, part)
		}
		mtPeek := strings.ToLower(p.Get("source.media_type").String())
		if idx := strings.Index(mtPeek, ";"); idx != -1 {
			mtPeek = strings.TrimSpace(mtPeek[:idx])
		}
		effectiveMT := mtPeek
		if effectiveMT == "" {
			if ptype == "document" {
				effectiveMT = "application/pdf"
			} else {
				effectiveMT = "image/png"
			}
		}
		isBinary := ptype == "image" && !isSVGMediaType(mtPeek)
		if ptype == "document" && srcTypePeek == "base64" {
			isBinary = (effectiveMT == "application/pdf" || strings.HasPrefix(effectiveMT, "image/")) && !isSVGMediaType(effectiveMT)
		}
		if isBinary {
			part := `{"text":""}`
			part, _ = sjson.Set(part, "text", "[Assistant output "+ptype+": "+effectiveMT+"]")
			return append(parts, part)
		}
	}

	if ptype == "document" {
		// Anthropic's document block's "title" and "context" fields (the
		// real filename, or a caller-supplied description) must be
		// surfaced — otherwise Gemini sees only raw inlineData bytes with
		// no name or description attached.
		title := p.Get("title").String()
		docContext := p.Get("context").String()
		if title != "" || docContext != "" {
			label := "[Attached Document"
			if title != "" {
				label += ": \"" + title + "\""
			}
			if docContext != "" {
				label += " - Context: " + docContext
			}
			label += "]"
			part := `{"text":""}`
			part, _ = sjson.Set(part, "text", label)
			parts = append(parts, part)
		}
	}

	src := p.Get("source")
	switch src.Get("type").String() {
	case "base64":
		if part, isBinary, text, ok := decodeAttachmentSource(ptype, src.Get("media_type").String(), src.Get("data").String()); ok {
			if isBinary {
				parts = append(parts, part)
			} else {
				textPart := `{"text":""}`
				textPart, _ = sjson.Set(textPart, "text", text)
				parts = append(parts, textPart)
			}
		}
	case "text":
		// A plain-text document source (source.data is the literal text,
		// not base64).
		if txt := src.Get("data").String(); txt != "" {
			part := `{"text":""}`
			part, _ = sjson.Set(part, "text", txt)
			parts = append(parts, part)
		}
	case "url":
		if u := src.Get("url").String(); u != "" {
			if role == "assistant" {
				part := `{"text":""}`
				part, _ = sjson.Set(part, "text", "[Assistant output "+ptype+" by URL: "+u+"]")
				parts = append(parts, part)
			} else if part, isBinary, text, ok := fetchAttachmentFromURL(ptype, u); ok {
				if isBinary {
					parts = append(parts, part)
				} else {
					textPart := `{"text":""}`
					textPart, _ = sjson.Set(textPart, "text", text)
					parts = append(parts, textPart)
				}
			} else {
				// Unreachable, blocked (SSRF guard), too large, or not
				// http(s) — fall back to surfacing the URL as text rather
				// than silence.
				part := `{"text":""}`
				part, _ = sjson.Set(part, "text", "[Attached "+ptype+" by URL (could not fetch): "+u+"]")
				parts = append(parts, part)
			}
		}
	case "content":
		// Anthropic's other text document form — source.type=="content",
		// where "content" is either the literal text or an array of
		// {"type":"text","text":...} blocks.
		contentField := src.Get("content")
		var combined strings.Builder
		if contentField.IsArray() {
			contentField.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "text" {
					combined.WriteString(block.Get("text").String())
				} else if block.Type == gjson.String {
					combined.WriteString(block.String())
				}
				return true
			})
		} else if contentField.Type == gjson.String {
			combined.WriteString(contentField.String())
		}
		if combined.Len() > 0 {
			part := `{"text":""}`
			part, _ = sjson.Set(part, "text", combined.String())
			parts = append(parts, part)
		}
	case "file":
		// Anthropic Files API reference ({"source":{"type":"file",
		// "file_id":"..."}}) — an audit caught this had no case at all,
		// so it silently vanished with nothing appended to parts. This
		// gateway never talks to api.anthropic.com (it only ever calls
		// Google), so there's no credential path to actually fetch the
		// file's real bytes from Anthropic's Files API here. Surfacing a
		// visible placeholder, same fallback shape the "url" case above
		// already uses for an unreachable URL, is strictly better than
		// silent, undetectable data loss — the model (and whoever reads
		// a capture later) can see an attachment existed instead of the
		// turn just looking shorter than the client sent.
		if fileID := src.Get("file_id").String(); fileID != "" {
			part := `{"text":""}`
			part, _ = sjson.Set(part, "text", "[Attached "+ptype+" via Anthropic Files API (file_id="+fileID+") — not retrievable through this gateway]")
			parts = append(parts, part)
		}
	}
	return parts
}

// appendAttachmentToToolResult handles one "image"/"document" entry of a
// tool_result's content array — writing title/context lines and decoded
// text into sb, or appending a ready inlineData part to imageParts for a
// genuinely binary attachment (Gemini's FunctionResponse message has no
// "parts" field, so a binary attachment can't nest inside the
// functionResponse part itself — see sealFunctionResponseTurn, which
// moves these into a separate following turn).
func appendAttachmentToToolResult(item gjson.Result, sb *strings.Builder, imageParts *[]string) {
	if title := item.Get("title").String(); title != "" {
		sb.WriteString("[Attached Document: \"" + title + "\"]\n")
	}
	if docContext := item.Get("context").String(); docContext != "" {
		sb.WriteString("[Context: " + docContext + "]\n")
	}
	ptype := item.Get("type").String()
	src := item.Get("source")
	switch src.Get("type").String() {
	case "base64":
		if part, isBinary, text, ok := decodeAttachmentSource(ptype, src.Get("media_type").String(), src.Get("data").String()); ok {
			if isBinary {
				*imageParts = append(*imageParts, part)
			} else {
				sb.WriteString(text)
			}
		}
	case "text":
		if txt := src.Get("data").String(); txt != "" {
			sb.WriteString(txt)
		}
	case "url":
		if u := src.Get("url").String(); u != "" {
			if part, isBinary, text, ok := fetchAttachmentFromURL(ptype, u); ok {
				if isBinary {
					*imageParts = append(*imageParts, part)
				} else {
					sb.WriteString(text)
				}
			} else {
				sb.WriteString("[Attached by URL (could not fetch): " + u + "]")
			}
		}
	case "content":
		contentField := src.Get("content")
		if contentField.IsArray() {
			contentField.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "text" {
					sb.WriteString(block.Get("text").String())
				} else if block.Type == gjson.String {
					sb.WriteString(block.String())
				}
				return true
			})
		} else if contentField.Type == gjson.String {
			sb.WriteString(contentField.String())
		}
	case "file":
		if fileID := src.Get("file_id").String(); fileID != "" {
			sb.WriteString("[Attached " + ptype + " via Anthropic Files API (file_id=" + fileID + ") — not retrievable through this gateway]")
		}
	}
}
