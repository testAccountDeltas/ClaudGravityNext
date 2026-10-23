// Part of the claude protocol package — see message.go for the package
// doc comment.
package claude

import "encoding/json"

// Tool is one tool declaration. InputSchema is the tool's JSON Schema —
// deliberately json.RawMessage, not a fixed struct: internal/schema
// (Part 6.6 of the spec) is the single owner of JSON Schema
// manipulation, and a schema's own shape is inherently dynamic
// (arbitrary nesting, vendor extensions, draft differences).
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
	// Type distinguishes a custom tool (empty/omitted) from an
	// Anthropic-hosted built-in (e.g. "web_search_20250305",
	// "bash_20250124", "text_editor_20250728") — the versioned built-in
	// tool names superrelay (v1)'s loopguard.go/translator.go both
	// explicitly recognize by prefix.
	Type string `json:"type,omitempty"`

	// CacheControl lets a prompt-caching breakpoint sit on a tool
	// declaration itself (not just on system/message blocks) — kept
	// dynamic like every other CacheControl field in this package, just
	// round-tripped untouched.
	CacheControl json.RawMessage `json:"cache_control,omitempty"`

	// Computer-use built-in tools (type "computer_20250124" etc.) declare
	// their target display's geometry directly on the tool object itself,
	// not inside InputSchema.
	DisplayWidthPx  int64 `json:"display_width_px,omitempty"`
	DisplayHeightPx int64 `json:"display_height_px,omitempty"`
	DisplayNumber   int64 `json:"display_number,omitempty"`

	// GAP-TL-13: Strict was missing entirely, so json.Unmarshal into
	// this struct silently dropped a client's "strict": true with no
	// trace of it surviving anywhere downstream.
	Strict bool `json:"strict,omitempty"`

	// Anthropic Tool Search and programmatic calling extensions
	DeferLoading        bool     `json:"defer_loading,omitempty"`
	AllowedCallers      []string `json:"allowed_callers,omitempty"`
	EagerInputStreaming bool     `json:"eager_input_streaming,omitempty"`
}
