// Package reqctx replaces superrelay (v1)'s 26/25-parameter
// executeStreamingWithWatchdog/executeNonStreaming signatures
// (server.go) — the migration spec's Part 1.4 diagnosis: a positional
// parameter list that long makes it trivial to pass two same-typed
// arguments in the wrong order with no compiler complaint, and GAP-D1-02
// (a real v1 incident: the streaming path's own prefill application was
// simply forgotten, because nothing forced every caller through one
// shared struct/function) is the concrete proof this isn't theoretical.
//
// This package holds ONLY these two data types — no Execute method, no
// net/http dependency. That's deliberate: it keeps reqctx a true leaf
// package that internal/httpapi, internal/orchestrate, internal/stream,
// and internal/translate can all import without any risk of an import
// cycle between them.
package reqctx

import "claudgravity/internal/config"

// Params holds one request's immutable parameters — created once in
// internal/httpapi's POST /v1/messages handler and passed by pointer
// (purely to avoid copying; the package's contract, enforced by review
// rather than the type system, is that nothing mutates it after
// creation). Carries every field v1's two giant positional signatures
// took, so failover/retry (internal/orchestrate/retry.go, once built) has
// one single struct to pass to a fresh attempt instead of threading 25
// loose values through by hand.
type Params struct {
	RID       uint64
	SessionID string
	ClientKey string
	AccLabel  string
	Tenant    config.TenantConfig
	ServerCfg config.ServerConfig
	ModelName string

	AgentKey     string
	AgentType    string
	SubagentName string
	InstanceID   string
	ConvAnchor   string

	IsStreaming   bool
	IsAntigravity bool
	WasShrunk     bool
	HasSafeguards bool
	HasPriorTurns bool
	PreferRussian bool
	// DisableParallelToolUse mirrors v1's wantsSingleToolUse(claudeBody) —
	// computed once here instead of re-parsing the raw request body at
	// every site that needs the answer (server.go called it from both
	// the streamer-construction site and the mid-stream single-tool-use
	// enforcement site).
	DisableParallelToolUse bool

	StopSequences        []string
	PrefillText          string
	SyntheticReasonTools map[string]bool
	ReverseToolNameMap   map[string]string

	RawClaudeBody []byte
}
