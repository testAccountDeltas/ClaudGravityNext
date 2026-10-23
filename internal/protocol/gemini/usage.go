// Part of the gemini protocol package — see content.go for the package
// doc comment. Split from response.go since UsageMetadata is used by
// both the streaming and non-streaming response paths as a standalone
// value, not just nested inside GenerateContentResponse.
package gemini

type UsageMetadata struct {
	PromptTokenCount        int64 `json:"promptTokenCount,omitempty"`
	CandidatesTokenCount    int64 `json:"candidatesTokenCount,omitempty"`
	TotalTokenCount         int64 `json:"totalTokenCount,omitempty"`
	CachedContentTokenCount int64 `json:"cachedContentTokenCount,omitempty"`
	ThoughtsTokenCount      int64 `json:"thoughtsTokenCount,omitempty"`
}
