// Part of the gemini protocol package — see content.go for the package
// doc comment.
package gemini

// FinishReason's values are the real Gemini generateContent enum
// (grounded in sse.go's own finishReason switch).
type FinishReason string

const (
	FinishUnspecified       FinishReason = "FINISH_REASON_UNSPECIFIED"
	FinishStop              FinishReason = "STOP"
	FinishMaxTokens         FinishReason = "MAX_TOKENS"
	FinishSafety            FinishReason = "SAFETY"
	FinishRecitation        FinishReason = "RECITATION"
	FinishProhibitedContent FinishReason = "PROHIBITED_CONTENT"
	FinishSPII              FinishReason = "SPII"
	FinishBlocklist         FinishReason = "BLOCKLIST"
)

// GenerateContentResponse is the body of a (non-streaming) generateContent
// response, or one accumulated/decoded chunk of a streaming one — each
// SSE chunk from Gemini has this same top-level shape, sometimes nested
// one level deeper under a "response" key (internal/stream's chunk
// decoder, once ported, is responsible for unwrapping that envelope the
// same way v1's sse.go already does).
type GenerateContentResponse struct {
	Candidates     []Candidate     `json:"candidates,omitempty"`
	PromptFeedback *PromptFeedback `json:"promptFeedback,omitempty"`
	UsageMetadata  *UsageMetadata  `json:"usageMetadata,omitempty"`
}

type Candidate struct {
	Content           Content            `json:"content"`
	FinishReason      FinishReason       `json:"finishReason,omitempty"`
	Index             int                `json:"index,omitempty"`
	GroundingMetadata *GroundingMetadata `json:"groundingMetadata,omitempty"`
}

// PromptFeedback carries why a prompt was blocked outright (no
// candidates generated at all) — distinct from a Candidate's own
// FinishReason, which fires after at least partial generation happened.
type PromptFeedback struct {
	BlockReason string `json:"blockReason,omitempty"`
}

// GroundingMetadata backs the web_search tool's results —
// groundingChunks' web.uri/web.title are what internal/websearch's
// (already ported) searchWithGoogleGrounding reads to build result
// items, and groundingSupports links specific summary sentences back to
// the chunk(s) that back them.
type GroundingMetadata struct {
	WebSearchQueries  []string           `json:"webSearchQueries,omitempty"`
	GroundingChunks   []GroundingChunk   `json:"groundingChunks,omitempty"`
	GroundingSupports []GroundingSupport `json:"groundingSupports,omitempty"`
}

type GroundingChunk struct {
	Web *GroundingChunkWeb `json:"web,omitempty"`
}

type GroundingChunkWeb struct {
	URI   string `json:"uri"`
	Title string `json:"title"`
}

type GroundingSupport struct {
	Segment               GroundingSegment `json:"segment"`
	GroundingChunkIndices []int            `json:"groundingChunkIndices,omitempty"`
}

type GroundingSegment struct {
	Text string `json:"text"`
}
