// Part of the claude protocol package — see message.go for the package
// doc comment. This file holds the non-tool content block types and
// Block's As* decoders — each one is the SINGLE point where a type
// assertion happens, returning an explicit ok bool instead of v1's
// pattern of a map[string]interface{} cast silently falling into a
// default: branch.
package claude

import "encoding/json"

// CacheControl's shape is a small closed set ({"type":"ephemeral",...}) but
// kept dynamic here rather than a fixed type: every block type below that
// can legally carry one just needs to round-trip it untouched through a
// decode-mutate-reencode cycle (internal/sanitize is the first caller that
// does this), not interpret it.
type TextBlock struct {
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
	// Citations is the array of citation objects a client may attach to
	// a text block it sends back (e.g. a document-citation-enabled
	// conversation's own prior turn) — kept dynamic and round-tripped
	// untouched like CacheControl above; this package doesn't interpret
	// citation shapes.
	Citations json.RawMessage `json:"citations,omitempty"`
}

// AsText decodes b as a TextBlock if b.Type == "text".
func (b Block) AsText() (TextBlock, bool) {
	if b.Type != "text" {
		return TextBlock{}, false
	}
	var t TextBlock
	_ = json.Unmarshal(b.Raw, &t)
	return t, true
}

// ThinkingBlock is Claude's extended-thinking content. Signature is the
// cryptographic signature Anthropic/Gemini attaches to a thinking block
// so it can be verified as genuine on a later turn (sse.go's
// thoughtSignature on the Gemini side maps to this on replay).
type ThinkingBlock struct {
	Type      string `json:"type"`
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
}

func (b Block) AsThinking() (ThinkingBlock, bool) {
	if b.Type != "thinking" {
		return ThinkingBlock{}, false
	}
	var t ThinkingBlock
	_ = json.Unmarshal(b.Raw, &t)
	return t, true
}

// RedactedThinkingBlock is a thinking block whose content Anthropic's
// safety systems withheld — Data is an opaque encrypted blob, never
// meant to be read, only round-tripped back on the next turn.
type RedactedThinkingBlock struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

func (b Block) AsRedactedThinking() (RedactedThinkingBlock, bool) {
	if b.Type != "redacted_thinking" {
		return RedactedThinkingBlock{}, false
	}
	var t RedactedThinkingBlock
	_ = json.Unmarshal(b.Raw, &t)
	return t, true
}

// ImageSource is the {"type":"base64"|"url"|"file", ...} source object nested
// inside an image/document block.
type ImageSource struct {
	Type      string `json:"type"` // "base64", "url", or "file"
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"` // base64, when Type=="base64"
	URL       string `json:"url,omitempty"`  // when Type=="url"
	FileID    string `json:"file_id,omitempty"`
}

type ImageBlock struct {
	Type         string          `json:"type"`
	Source       ImageSource     `json:"source"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

func (b Block) AsImage() (ImageBlock, bool) {
	if b.Type != "image" {
		return ImageBlock{}, false
	}
	var t ImageBlock
	_ = json.Unmarshal(b.Raw, &t)
	return t, true
}

// DocumentSource mirrors ImageSource but also allows Type=="content" (a
// pre-extracted array of blocks instead of raw file bytes) and "text"
// (a plain-text document, e.g. a converted PDF page).
type DocumentSource struct {
	Type      string          `json:"type"` // "base64", "url", "content", "text", or "file"
	MediaType string          `json:"media_type,omitempty"`
	Data      string          `json:"data,omitempty"`
	URL       string          `json:"url,omitempty"`
	FileID    string          `json:"file_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // dynamic: array of blocks when Type=="content"
}

type DocumentBlock struct {
	Type         string          `json:"type"`
	Source       DocumentSource  `json:"source"`
	Title        string          `json:"title,omitempty"`
	Context      string          `json:"context,omitempty"`
	Citations    json.RawMessage `json:"citations,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

func (b Block) AsDocument() (DocumentBlock, bool) {
	if b.Type != "document" {
		return DocumentBlock{}, false
	}
	var t DocumentBlock
	_ = json.Unmarshal(b.Raw, &t)
	return t, true
}
