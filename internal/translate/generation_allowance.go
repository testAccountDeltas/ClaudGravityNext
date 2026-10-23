// Part of the translate package — see translate.go for the package doc
// comment. Output-token allowance for models whose thinking cannot be
// switched off. Split from generation.go to stay under the file-size
// ceiling.
package translate

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/accounts"
)

// InternalReasoningAllowance is how many extra maxOutputTokens (on top of
// the client's max_tokens) a request gets when the model cannot disable
// thinking and the client did not ask for any. Gemini shares one
// maxOutputTokens ceiling between hidden thinking and the answer; live logs
// of gemini-3.8-flash showed unavoidable thinking of ~200-1300 tokens even
// at the model's minimum thinkingBudget, so anything near the budget floor
// (the earlier +128) truncated small-max_tokens calls (Claude Code
// title/summary, max_tokens 32-512) in the middle of the answer. Thinking
// itself is still capped by thinkingBudget; this only guarantees the
// visible answer is not starved. Whatever the model does not use costs
// nothing on this quota backend, and the client's max_tokens is enforced on
// visible text separately (internal/stream/visiblecap.go).
const InternalReasoningAllowance = 1024

// internalAllowance is the allowance for a model with thinking floor.
func internalAllowance(floor int) int {
	if floor > InternalReasoningAllowance {
		return floor
	}
	return InternalReasoningAllowance
}

func outputCeiling(meta accounts.AvailableModelInfo, hasMeta bool) int64 {
	if hasMeta && meta.MaxOutput > 0 {
		return meta.MaxOutput
	}
	return 64000
}

// BumpOutputAllowance widens generationConfig.maxOutputTokens of an
// already-built Gemini body by one more InternalReasoningAllowance (so the
// allowance is doubled), clamped to the model's output ceiling. Used for the
// single retry of a turn that ended with MAX_TOKENS and no visible output
// because hidden thinking consumed the whole allowance. ok=false means the
// body already sits at the ceiling (or has no maxOutputTokens) and cannot be
// widened.
func BumpOutputAllowance(body []byte, modelName string) (newBody []byte, ok bool) {
	cur := gjson.GetBytes(body, "generationConfig.maxOutputTokens").Int()
	if cur <= 0 {
		return body, false
	}
	meta, hasMeta := accounts.ModelCatalog().GetModel(modelName)
	next := cur + InternalReasoningAllowance
	if ceiling := outputCeiling(meta, hasMeta); next > ceiling {
		next = ceiling
	}
	if next <= cur {
		return body, false
	}
	out, err := sjson.SetBytes(body, "generationConfig.maxOutputTokens", next)
	if err != nil {
		return body, false
	}
	return out, true
}
