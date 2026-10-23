// Part of the gemini protocol package — see content.go for the package
// doc comment.
package gemini

import "encoding/json"

// FunctionDeclaration is one tool's schema, as internal/schema (Part 6.6
// of the spec) produces it from a claude.Tool. Parameters stays
// json.RawMessage — internal/schema is the single owner of JSON Schema
// shape, not this package.
type FunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Tool is one entry in a request's "tools" array — either a set of
// function declarations, or the built-in googleSearch grounding tool
// (mutually exclusive on the wire; Google rejects combining them).
type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations,omitempty"`
	GoogleSearch         *GoogleSearchTool     `json:"googleSearch,omitempty"`
}

// GoogleSearchTool is always an empty object on the wire — its mere
// presence (any non-nil pointer) is what enables grounding, not any
// field inside it.
type GoogleSearchTool struct{}

// ToolConfig controls how the model is allowed to call tools.
type ToolConfig struct {
	FunctionCallingConfig *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
}

// FunctionCallingConfig's Mode is one of "AUTO", "ANY", "NONE" —
// AllowedFunctionNames narrows ANY mode to a specific subset.
type FunctionCallingConfig struct {
	Mode                 string   `json:"mode,omitempty"`
	AllowedFunctionNames []string `json:"allowedFunctionNames,omitempty"`
}
