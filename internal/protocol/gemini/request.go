// Part of the gemini protocol package — see content.go for the package
// doc comment.
package gemini

import "encoding/json"

// GenerateContentRequest is the body of a v1internal:generateContent
// call. Note it's often wrapped one level deeper inside Antigravity's
// own envelope ({"project":...,"model":...,"request":{...this...}}) —
// internal/warmup.WrapAntigravityPayload (already ported) owns that
// outer wrapping; this type is the inner payload.
type GenerateContentRequest struct {
	Contents          []Content         `json:"contents"`
	SystemInstruction *Content          `json:"systemInstruction,omitempty"`
	Tools             []Tool            `json:"tools,omitempty"`
	ToolConfig        *ToolConfig       `json:"toolConfig,omitempty"`
	GenerationConfig  *GenerationConfig `json:"generationConfig,omitempty"`
	SafetySettings    []SafetySetting   `json:"safetySettings,omitempty"`
}

type GenerationConfig struct {
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"topP,omitempty"`
	TopK             *float64        `json:"topK,omitempty"`
	MaxOutputTokens  int64           `json:"maxOutputTokens,omitempty"`
	StopSequences    []string        `json:"stopSequences,omitempty"`
	ResponseMimeType string          `json:"responseMimeType,omitempty"`
	ResponseSchema   json.RawMessage `json:"responseSchema,omitempty"` // internal/schema's output; dynamic for the same reason Tool.Parameters is
	ThinkingConfig   *ThinkingConfig `json:"thinkingConfig,omitempty"`
}

// ThinkingConfig controls extended-thinking behavior. ThinkingBudget's
// sentinel values (-1 = ModeAuto/unlimited, 0 = disabled, >0 = explicit
// token cap) are internal/accounts/tiering's (already ported) output
// contract — this type just carries whatever that package decided.
type ThinkingConfig struct {
	IncludeThoughts bool  `json:"includeThoughts,omitempty"`
	ThinkingBudget  int64 `json:"thinkingBudget"`
}

// SafetySetting is one entry controlling a harm category's blocking
// threshold — Category values are the real HARM_CATEGORY_* strings
// Google's API expects (grounded in translator.go's own constant list).
type SafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"` // "BLOCK_NONE", "OFF", etc.
}
