// Part of the claude protocol package — see message.go for the package
// doc comment. This file holds the outbound SSE event payload shapes —
// the exact sequence internal/stream's StreamFSM (Part 6.3 of the spec)
// must emit: message_start -> (content_block_start -> content_block_delta*
// -> content_block_stop)* -> message_delta -> message_stop.
package claude

import "encoding/json"

type MessageStartEvent struct {
	Type    string          `json:"type"` // "message_start"
	Message MessageStartMsg `json:"message"`
}

type MessageStartMsg struct {
	ID           string      `json:"id"`
	Type         string      `json:"type"` // "message"
	Role         Role        `json:"role"`
	Model        string      `json:"model"`
	Content      []Block     `json:"content"` // always empty at message_start
	StopReason   *StopReason `json:"stop_reason"`
	StopSequence *string     `json:"stop_sequence"`
	Usage        Usage       `json:"usage"`
}

type ContentBlockStartEvent struct {
	Type         string          `json:"type"` // "content_block_start"
	Index        int             `json:"index"`
	ContentBlock json.RawMessage `json:"content_block"` // the block's starting shape (e.g. {"type":"text","text":""}) — RawMessage since its exact starting fields vary by block type
}

type CitationDelta struct {
	Type            string `json:"type"` // "char_location" | "page_location" | "content_block_location"
	CitedText       string `json:"cited_text,omitempty"`
	DocumentIndex   int    `json:"document_index"`
	DocumentTitle   string `json:"document_title,omitempty"`
	StartCharIndex  int    `json:"start_char_index,omitempty"`
	EndCharIndex    int    `json:"end_char_index,omitempty"`
	StartPageNumber int    `json:"start_page_number,omitempty"`
	EndPageNumber   int    `json:"end_page_number,omitempty"`
	StartBlockIndex int    `json:"start_block_index,omitempty"`
	EndBlockIndex   int    `json:"end_block_index,omitempty"`
}

type ContentBlockDelta struct {
	Type string `json:"type"` // "text_delta" | "input_json_delta" | "thinking_delta" | "signature_delta" | "citations_delta"
	// Exactly one of these is populated, selected by Type.
	Text        string         `json:"text,omitempty"`
	PartialJSON string         `json:"partial_json,omitempty"`
	Thinking    string         `json:"thinking,omitempty"`
	Signature   string         `json:"signature,omitempty"`
	Citation    *CitationDelta `json:"citation,omitempty"`
}

type ContentBlockDeltaEvent struct {
	Type  string            `json:"type"` // "content_block_delta"
	Index int               `json:"index"`
	Delta ContentBlockDelta `json:"delta"`
}

type ContentBlockStopEvent struct {
	Type  string `json:"type"` // "content_block_stop"
	Index int    `json:"index"`
}

type MessageDelta struct {
	StopReason       StopReason      `json:"stop_reason"`
	StopSequence     *string         `json:"stop_sequence"`
	StopDetails      *StopDetails    `json:"stop_details,omitempty"`
	SafeguardResults json.RawMessage `json:"safeguard_results,omitempty"` // Claude Code's own extension; shape not part of the stable Anthropic contract
}

type MessageDeltaUsage struct {
	OutputTokens        int64                `json:"output_tokens"`
	OutputTokensDetails *OutputTokensDetails `json:"output_tokens_details,omitempty"`
}

type MessageDeltaEvent struct {
	Type  string            `json:"type"` // "message_delta"
	Delta MessageDelta      `json:"delta"`
	Usage MessageDeltaUsage `json:"usage"`
}

type MessageStopEvent struct {
	Type string `json:"type"` // "message_stop"
}

type PingEvent struct {
	Type string `json:"type"` // "ping"
}
