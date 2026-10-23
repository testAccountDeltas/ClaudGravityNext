// Part of the reqctx package — see params.go for the package doc comment.
package reqctx

import (
	"time"

	"claudgravity/internal/accounts"
)

// State holds one ATTEMPT's mutable state — explicitly rebuilt from
// scratch on every retry/failover to a different account
// (internal/orchestrate/retry.go, once built), never carried over or
// reused from the previous attempt. v1 had no equivalent boundary: several
// of server.go's local variables inside its retry loop were simply
// reused across iterations, leaving an open class of "stuck state from
// the previous account" bugs (a stale BearerToken or GeminiBody surviving
// into a retry against a DIFFERENT account would silently misattribute
// billing/auth to the wrong account, or send a tiering decision made for
// account A's tier to account B). Rebuilding a fresh State per attempt
// makes that bug class structurally impossible rather than relying on
// every future contributor to remember to reset every field by hand.
type State struct {
	Attempt   int
	StartedAt time.Time

	ActiveAccount *accounts.Record
	BearerToken   string
	UpstreamURL   string

	// ResolvedModelName is the Gemini-side model tiering actually picked
	// for THIS attempt — distinct from Params.ModelName (the client's
	// original request, e.g. "claude-opus-4-5"), since different retry
	// attempts can land on different accounts with different tiers/quota
	// state and so can resolve to different Gemini sub-models. v1
	// conflated these into a single reused `modelName` variable, first
	// holding the client's name, then reassigned mid-function to the
	// tiered Gemini name — the same variable meaning two different things
	// across one function body is exactly the ambiguity a dedicated field
	// per concern avoids.
	ResolvedModelName string

	GeminiBody []byte

	EstInputTokens  int
	CachePrefixFrac float64
}
