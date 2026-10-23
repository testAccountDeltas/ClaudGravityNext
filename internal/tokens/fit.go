// Part of the tokens package — see text.go for the package doc comment.
// Context-window fit check: decides whether a request can be sent at all,
// independent of whether history truncation is enabled.
package tokens

import (
	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/sanitize"
)

// ContextBudget is the maximum INPUT token count a request may have: the
// model window minus the reserved output and the estimator safety margin.
func ContextBudget(maxTokens, requestedMaxOutput int64) int64 {
	budget := maxTokens - requestedMaxOutput - ContextSafetyMarginTokens
	if budget <= 0 {
		budget = maxTokens / 2
	}
	return budget
}

// EstimateForFit returns the weighted input-token estimate used to
// compare against ContextBudget (repeated Reads priced at their dedup
// stub, like TruncateConversationToFit does).
func EstimateForFit(req claude.Request, prof Profile, sessionID string) int {
	evalReq, _ := sanitize.DedupRepeatedReads(req, sessionID)
	return WeightedRequestTokensFor(evalReq, prof)
}
