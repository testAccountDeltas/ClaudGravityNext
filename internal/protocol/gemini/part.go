// Part of the gemini protocol package — see content.go for the package
// doc comment. Part is the union type for one piece of a Content's
// Parts array. Unlike internal/protocol/claude's Block, Gemini's wire
// format has no explicit "type" discriminator field at all — which
// concrete shape a Part is is determined by WHICH key is present
// (text/functionCall/functionResponse/inlineData), so Part keeps every
// field inline (all omitempty) rather than a Raw-bytes-plus-discriminator
// pair the way Block does.
package gemini

import "encoding/json"

type Part struct {
	Text string `json:"text,omitempty"`

	FunctionCall *FunctionCall `json:"functionCall,omitempty"`

	FunctionResponse *FunctionResponse `json:"functionResponse,omitempty"`

	InlineData *InlineData `json:"inlineData,omitempty"`

	// Thought marks this part as the model's own reasoning (extended
	// thinking) rather than user-visible output.
	Thought bool `json:"thought,omitempty"`

	// ThoughtSignature is Google's cryptographic signature over a
	// thinking part, required on replay for the model to trust its own
	// prior reasoning was genuine and not tampered with.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

// FunctionCall is a model-issued tool invocation. Args is deliberately
// json.RawMessage — per Part 3 p.3 of the spec, a tool's argument shape
// is defined by whatever schema the caller declared, not something this
// package should pin down statically.
type FunctionCall struct {
	ID   string          `json:"id,omitempty"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// FunctionResponse carries a tool's result back to the model. Response
// is dynamic for the same reason Args is — its shape depends entirely
// on what the tool returned.
type FunctionResponse struct {
	ID       string          `json:"id,omitempty"`
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// InlineData is base64-encoded binary content (an image, a document)
// embedded directly in the request.
type InlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}
