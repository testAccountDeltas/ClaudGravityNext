// Part of the httpapi package — see errors.go for the package doc
// comment. Request-class fallback detection, SSE response hardening and
// the explicit ignore-policy for unsupported request fields.
package httpapi

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"claudgravity/internal/accounts/tiering"
	"claudgravity/internal/editrepair"
	"claudgravity/internal/logx"
)

// classifyReqClass returns the effective request class for tiering. The
// x-claude-code-request-class header wins; without it, a parent-agent-id
// header marks a subagent and small tool-less helper calls (title,
// summary, topic, classifier) are classed "auxiliary" by heuristics.
// x-claude-code-context-compacted only affects logging: a compacted
// context carries no tier implication of its own.
func classifyReqClass(r *http.Request, reqClass string, body []byte) string {
	if reqClass != "" {
		return reqClass
	}
	if r.Header.Get("x-claude-code-parent-agent-id") != "" {
		return "subagent"
	}
	if v := r.Header.Get("x-claude-code-context-compacted"); v != "" && v != "0" && !strings.EqualFold(v, "false") {
		logx.LogGateway("[httpapi] request follows a context compaction (x-claude-code-context-compacted=%s)", v)
	}
	tools := gjson.GetBytes(body, "tools")
	hasTools := tools.IsArray() && len(tools.Array()) > 0
	return tiering.ClassifyRequestClass("", gjson.GetBytes(body, "model").String(),
		gjson.GetBytes(body, "max_tokens").Int(), hasTools, editrepair.ExtractSystemPromptText(body))
}

// ignoredRequestFields are accepted-and-ignored: no Gemini equivalent, and
// Claude Code's own retry logic only expects 400/422 for fields that truly
// block the request.
var ignoredRequestFields = []string{
	"mcp_servers", "service_tier", "speed",
	"inference_geo", "container", "betas",
}

func logIgnoredFields(rid uint64, body []byte) {
	var present []string
	for _, f := range ignoredRequestFields {
		if f == "betas" {
			continue // merged from the anthropic-beta header, consumed by translate
		}
		if gjson.GetBytes(body, f).Exists() {
			present = append(present, f)
		}
	}
	if len(present) > 0 {
		logx.LogGateway("[#%d] ignoring unsupported request field(s): %s", rid, strings.Join(present, ", "))
	}
}

// setSSEHeaders pre-sets the response headers a streaming client behind a
// buffering proxy needs. Connection is HTTP/1.x only (forbidden in HTTP/2).
func setSSEHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	if r.ProtoMajor == 1 {
		h.Set("Connection", "keep-alive")
	}
}
