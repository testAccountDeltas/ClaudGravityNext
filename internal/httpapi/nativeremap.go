// Part of the httpapi package — see errors.go for the package doc
// comment. remapExhaustedNative3PModel is a faithful port of v1's
// handleMessages section "pre-translation native 3P model remap": native
// 3P model names (claude-sonnet-4-6 etc.) pass through
// internal/accounts/tiering.ResolveTieringAndEffort unchanged — fine
// when the account serving this request has 3P quota, but free-tier
// accounts commonly sit at 0% there while Gemini itself is wide open.
// Real account selection for the request happens later (translation has
// to run first — it decides the model that selection routes on), so this
// peeks the conversation's already-sticky account, if any, to catch the
// common case: an established multi-turn chat about to land back on the
// same exhausted account it was already using.
package httpapi

import (
	"claudgravity/internal/accounts"
	"claudgravity/internal/logx"
	"claudgravity/internal/ratelimit"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func remapExhaustedNative3PModel(rid uint64, convAnchor string, bodyBytes []byte) []byte {
	reqModel := gjson.GetBytes(bodyBytes, "model").String()
	if reqModel != "claude-sonnet-4-6" && reqModel != "claude-opus-4-6-thinking" && reqModel != "gpt-oss-120b-medium" {
		return bodyBytes
	}
	peekEmail := accounts.GlobalSelector().GetAffinityAccount(convAnchor)
	if peekEmail == "" {
		return bodyBytes
	}
	peekAcc, ok := accounts.Manager().GetAccount(peekEmail)
	if !ok || !ratelimit.IsNative3PModelExhausted(peekAcc) {
		return bodyBytes
	}
	patched, err := sjson.SetBytes(bodyBytes, "model", "gemini-3.8-flash-tiered")
	if err != nil {
		return bodyBytes
	}
	logx.LogGateway("[#%d] !! Native 3P model %s has exhausted quota on sticky account %s, remapping to gemini-3.8-flash-tiered", rid, reqModel, peekEmail)
	return patched
}
