// Part of the claude protocol package — see message.go for the package
// doc comment.
package claude

import (
	"encoding/json"
	"strings"
)

// SystemPrompt decodes either shape Claude Code sends: a bare string,
// or an array of {"type":"text","text":...,"cache_control":...} blocks.
// Kept as RawMessage at the Request level (below) rather than a fixed
// type, since callers that only care about the plain text (most of
// them) want a single decode step, not a second union type to unwrap —
// see Request.SystemText().
type SystemPrompt json.RawMessage

// MarshalJSON/UnmarshalJSON are required because a Go type DEFINITION
// (as opposed to an alias) does not inherit the underlying type's
// methods — without these, SystemPrompt silently falls back to default
// []byte JSON handling (base64 for a string, element-by-element uint8
// decoding for an array), which is exactly why Claude Code's
// array-of-blocks system prompt shape ([{"type":"text",...}]) failed
// with "cannot unmarshal object into Request.system.0 of type uint8"
// in production the first time a real client sent it: the bare-string
// shape happens to decode without error via that same fallback (base64
// of a plain string often "succeeds" producing garbage, or the field
// is simply empty in quick tests), masking the bug until a real system
// array arrived. Both methods delegate to json.RawMessage's own
// (tested, correct) implementation.
func (s *SystemPrompt) UnmarshalJSON(data []byte) error {
	return (*json.RawMessage)(s).UnmarshalJSON(data)
}

func (s SystemPrompt) MarshalJSON() ([]byte, error) {
	return json.RawMessage(s).MarshalJSON()
}

// Request is the top-level POST /v1/messages body.
type Request struct {
	Model         string          `json:"model"`
	Messages      []Message       `json:"messages"`
	System        SystemPrompt    `json:"system,omitempty"`
	MaxTokens     int64           `json:"max_tokens"`
	Stream        bool            `json:"stream,omitempty"`
	Tools         []Tool          `json:"tools,omitempty"`
	ToolChoice    json.RawMessage `json:"tool_choice,omitempty"` // {"type":"auto"|"any"|"tool",...} — small, closed set, but kept dynamic to avoid a third near-duplicate type for something translate.go/orchestrate can switch on directly
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Thinking      json.RawMessage `json:"thinking,omitempty"` // {"type":"enabled"|"disabled"|"adaptive","budget_tokens":...} — see internal/accounts/tiering's handling of this exact shape

	// Metadata is kept dynamic (GAP-T2): a fixed {user_id,session_id}
	// struct silently dropped any other field a caller sent under
	// "metadata" (e.g. a custom "project_id"/"environment") on the
	// struct round-trip translate.go does before Gemini translation.
	// Nothing in this codebase currently reads a field off this one by
	// name — internal/httpapi/messages.go's own metadata.session_id
	// fallback reads the RAW request body via gjson, before this struct
	// round-trip ever runs — so this is round-trip-safety only.
	Metadata json.RawMessage `json:"metadata,omitempty"`

	// GAP-T1: service_tier had no field at all, so it was silently
	// dropped before ever reaching the point generation.go could even
	// consider it — round-trip-safety only, same as Metadata above;
	// nothing currently maps it to a Gemini-side equivalent (Gemini has
	// no analogous concept on this quota-based backend).
	ServiceTier string `json:"service_tier,omitempty"`

	// OutputConfig is Claude Code's own proprietary extension
	// (output_config.effort), not a documented Anthropic API field —
	// kept dynamic since its shape is this client's own, not a stable
	// public contract.
	OutputConfig json.RawMessage `json:"output_config,omitempty"`

	// Sampling hyperparameters. Pointers (not bare float64/int64) so an
	// absent field round-trips as absent rather than as an explicit 0 —
	// translate/generation.go's applyGenerationConfig only forwards a
	// hyperparameter when the client actually sent one.
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"top_p,omitempty"`
	TopK        *int64   `json:"top_k,omitempty"`

	// DisableParallelToolUse is the top-level form of this flag. Official
	// SDKs normally nest it under tool_choice instead
	// (translate/system.go already reads tool_choice.disable_parallel_tool_use
	// from ToolChoice), but the Anthropic API also accepts it here directly.
	DisableParallelToolUse *bool `json:"disable_parallel_tool_use,omitempty"`

	// OutputFormat is the top-level form client.messages.parse() sends
	// (as opposed to the nested output_config.format Claude Code's own
	// CLI uses) — translate/generation.go's applyGenerationConfig already
	// falls back to reading this field when output_config.format is
	// absent; it just needs to survive the struct round-trip to get there.
	OutputFormat json.RawMessage `json:"output_format,omitempty"`

	// Top-level extensions
	CacheControl      json.RawMessage `json:"cache_control,omitempty"`
	Speed             string          `json:"speed,omitempty"`
	InferenceGeo      json.RawMessage `json:"inference_geo,omitempty"`
	ContextManagement json.RawMessage `json:"context_management,omitempty"`
	Container         json.RawMessage `json:"container,omitempty"`
	Betas             []string        `json:"betas,omitempty"`
}

// SystemText decodes System into plain text regardless of which shape
// it arrived in, joining multiple blocks with "\n" — the same decoding
// rule superrelay (v1)'s ExtractSystemPromptText uses.
func (r Request) SystemText() string {
	if len(r.System) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(r.System, &s); err == nil {
		return s
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(r.System, &blocks); err == nil {
		out := ""
		for _, b := range blocks {
			out += b.Text + "\n"
		}
		return out
	}
	var strs []string
	if err := json.Unmarshal(r.System, &strs); err == nil {
		return strings.Join(strs, "\n")
	}
	return ""
}
