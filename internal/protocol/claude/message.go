// Package claude is a NEW typed domain for the Anthropic Messages API —
// not a port of any single v1 file, called for by
// ARCHITECTURE_V2_SPECIFICATION_AND_MIGRATION_PLAN.md Part 5.1. v1
// represents every Claude message/block as map[string]interface{},
// mutated via gjson/sjson — the diagnosed Part 1.1 failure mode (a type
// mismatch between two passes over the same data is a silent runtime
// no-op, not a compile error). This package fixes that for the
// protocol FRAME (roles, block discriminators, usage, stop_reason);
// per Part 3 p.3, a tool's own `input` and any JSON Schema body stay
// json.RawMessage — those are genuinely dynamic, not something a fixed
// struct should pretend to pin down.
//
// Grounded in the actual field names superrelay (v1) reads/writes
// (grepped from translator.go/sse.go/server.go before writing this),
// not invented from the public API docs alone.
package claude

import "encoding/json"

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is one turn of the conversation. Content is always normalized
// to an array of Blocks on decode, even though Anthropic's own API also
// allows a bare string as shorthand for a single text block (grounded in
// translator.go's own Step 1, which explicitly switches on that shape) —
// ContentWasBareString records which form arrived, for a caller (like
// internal/sanitize) whose own behavior genuinely differs between the two.
// MarshalJSON always re-emits the array form, matching v1's own behavior
// of normalizing to an array on every output regardless of input shape.
type Message struct {
	Role                 Role            `json:"role"`
	Content              []Block         `json:"content"`
	ContentWasBareString bool            `json:"-"`
	OutputConfig         json.RawMessage `json:"output_config,omitempty"`
}

func (m *Message) UnmarshalJSON(data []byte) error {
	var disc struct {
		Role         Role            `json:"role"`
		Content      json.RawMessage `json:"content"`
		OutputConfig json.RawMessage `json:"output_config,omitempty"`
	}
	if err := json.Unmarshal(data, &disc); err != nil {
		return err
	}
	m.Role = disc.Role
	m.OutputConfig = disc.OutputConfig
	if len(disc.Content) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(disc.Content, &s); err == nil {
		m.Content = []Block{NewBlock(TextBlock{Type: "text", Text: s})}
		m.ContentWasBareString = true
		return nil
	}
	var blocks []Block
	if err := json.Unmarshal(disc.Content, &blocks); err != nil {
		return err
	}
	m.Content = blocks
	return nil
}

func (m Message) MarshalJSON() ([]byte, error) {
	type wire struct {
		Role         Role            `json:"role"`
		Content      []Block         `json:"content"`
		OutputConfig json.RawMessage `json:"output_config,omitempty"`
	}
	return json.Marshal(wire{Role: m.Role, Content: m.Content, OutputConfig: m.OutputConfig})
}

// Block is the union type for one content block. Type carries the
// discriminator ("text", "thinking", "tool_use", "tool_result",
// "image", "document", "redacted_thinking", "server_tool_use",
// "web_search_tool_result"); Raw carries the exact source bytes, so
// re-marshaling a block this package doesn't specifically decode (a
// future Claude block type, or one deliberately left opaque) round-trips
// byte-for-byte instead of silently dropping fields a plain struct
// didn't know about.
type Block struct {
	Type string          `json:"type"`
	Raw  json.RawMessage `json:"-"`
}

func (b *Block) UnmarshalJSON(data []byte) error {
	var disc struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &disc); err != nil {
		return err
	}
	b.Type = disc.Type
	b.Raw = append(json.RawMessage(nil), data...)
	return nil
}

func (b Block) MarshalJSON() ([]byte, error) {
	if b.Raw == nil {
		return []byte("null"), nil
	}
	return b.Raw, nil
}

// NewBlock marshals v (one of this package's typed block structs, e.g.
// TextBlock or ToolResultBlock) into a Block, re-deriving Type from v's own
// "type" field so a caller building a block from scratch — internal/sanitize
// is the first one — never has to keep a separate discriminator string in
// sync with the struct literal by hand.
func NewBlock(v interface{}) Block {
	raw, err := json.Marshal(v)
	if err != nil {
		return Block{}
	}
	var disc struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &disc)
	return Block{Type: disc.Type, Raw: raw}
}
