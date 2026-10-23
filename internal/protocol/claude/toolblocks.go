// Part of the claude protocol package — see message.go for the package
// doc comment. This file holds the tool-related block types, whose
// dynamic parts (a tool's own arguments, a tool_result's content, which
// can be a plain string OR an array of blocks) stay json.RawMessage per
// Part 3 p.3 of the spec — a tool's argument shape is defined by
// whatever schema the CALLER declared, not something this package can
// pin down statically.
package claude

import "encoding/json"

type ToolUseBlock struct {
	Type         string          `json:"type"`
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Input        json.RawMessage `json:"input"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

func (b Block) AsToolUse() (ToolUseBlock, bool) {
	if b.Type != "tool_use" {
		return ToolUseBlock{}, false
	}
	var t ToolUseBlock
	_ = json.Unmarshal(b.Raw, &t)
	return t, true
}

// ToolResultBlock's Content is dynamic: either a bare string, or an
// array of content blocks (typically text/image). Use
// ToolResultBlock.ContentBlocks() to decode the array case without a
// manual type assertion at the call site.
type ToolResultBlock struct {
	Type         string          `json:"type"`
	ToolUseID    string          `json:"tool_use_id"`
	IsError      bool            `json:"is_error,omitempty"`
	Content      json.RawMessage `json:"content"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

func (b Block) AsToolResult() (ToolResultBlock, bool) {
	if b.Type != "tool_result" {
		return ToolResultBlock{}, false
	}
	var t ToolResultBlock
	_ = json.Unmarshal(b.Raw, &t)
	return t, true
}

// ContentText returns Content decoded as a plain string, if that's the
// shape it's in.
func (t ToolResultBlock) ContentText() (string, bool) {
	var s string
	if err := json.Unmarshal(t.Content, &s); err != nil {
		return "", false
	}
	return s, true
}

// ContentBlocks returns Content decoded as an array of Blocks, if that's
// the shape it's in.
func (t ToolResultBlock) ContentBlocks() ([]Block, bool) {
	var blocks []Block
	if err := json.Unmarshal(t.Content, &blocks); err != nil {
		return nil, false
	}
	return blocks, true
}

// ServerToolUseBlock is a gateway-synthesized tool call the client never
// actually requested execution of itself (this gateway's own web_search
// implementation emits one) — same shape as ToolUseBlock, distinct type
// name for clarity at call sites that specifically expect this kind.
type ServerToolUseBlock struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func (b Block) AsServerToolUse() (ServerToolUseBlock, bool) {
	if b.Type != "server_tool_use" {
		return ServerToolUseBlock{}, false
	}
	var t ServerToolUseBlock
	_ = json.Unmarshal(b.Raw, &t)
	return t, true
}

// WebSearchResultItem is one entry in a web_search_tool_result's Content
// array.
type WebSearchResultItem struct {
	Type             string `json:"type"` // "web_search_result"
	Title            string `json:"title"`
	URL              string `json:"url"`
	Snippet          string `json:"snippet,omitempty"`
	EncryptedContent string `json:"encrypted_content,omitempty"`
	PageAge          string `json:"page_age,omitempty"`
}

type WebSearchToolResultBlock struct {
	Type      string                `json:"type"`
	ToolUseID string                `json:"tool_use_id"`
	Content   []WebSearchResultItem `json:"content"`
}

func (b Block) AsWebSearchToolResult() (WebSearchToolResultBlock, bool) {
	if b.Type != "web_search_tool_result" {
		return WebSearchToolResultBlock{}, false
	}
	var t WebSearchToolResultBlock
	_ = json.Unmarshal(b.Raw, &t)
	return t, true
}
