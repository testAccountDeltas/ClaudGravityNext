package ctxedit

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/protocol/claude"
	"claudgravity/internal/tokens"
)

// ToolResultPlaceholder replaces the content of a cleared tool result.
const ToolResultPlaceholder = "[Tool result cleared to save context. Re-run the tool if its output is needed again.]"

// ThinkingPlaceholder stands in for an assistant turn whose only content
// was thinking.
const ThinkingPlaceholder = "(thinking cleared)"

// Result is the outcome of Apply.
type Result struct {
	Body           []byte        // the request body after editing
	Applied        []AppliedEdit // entries for context_management.applied_edits
	OriginalTokens int           // estimated prompt tokens before editing
	Tokens         int           // estimated prompt tokens after editing
}

// Apply runs the configured strategies (thinking first, as the spec
// requires) over the request's messages. The client keeps its full
// history, so the same edits are recomputed on every request.
func Apply(body []byte, cfg Config) (Result, error) {
	res := Result{Body: body, OriginalTokens: estimatePrompt(body)}
	res.Tokens = res.OriginalTokens
	if cfg.Thinking == nil && cfg.ToolUses == nil {
		return res, nil
	}
	raw := gjson.GetBytes(body, "messages").Raw
	var msgs []interface{}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	if err := dec.Decode(&msgs); err != nil {
		return res, nil
	}
	changed := false
	if cfg.Thinking != nil {
		if e, ok := clearThinking(msgs, *cfg.Thinking); ok {
			res.Applied = append(res.Applied, e)
			changed = true
		}
	}
	if cfg.ToolUses != nil {
		if e, ok := clearToolUses(msgs, *cfg.ToolUses, res.OriginalTokens); ok {
			res.Applied = append(res.Applied, e)
			changed = true
		}
	}
	if !changed {
		return res, nil
	}
	out, err := marshalLiteral(msgs)
	if err != nil {
		return res, err
	}
	nb, err := sjson.SetRawBytes(body, "messages", out)
	if err != nil {
		return res, err
	}
	res.Body = nb
	res.Tokens = estimatePrompt(nb)
	return res, nil
}

// marshalLiteral encodes without HTML escaping so '<', '>' and '&' in the
// client's text stay literal (json.Marshal would escape them as backslash-u sequences).
// Numbers survive exactly because the decoder used json.Number.
func marshalLiteral(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// StripField removes context_management from a body once handled so the
// translation layer does not log it as an ignored field.
func StripField(body []byte) []byte {
	nb, err := sjson.DeleteBytes(body, "context_management")
	if err != nil {
		return body
	}
	return nb
}

func estimatePrompt(body []byte) int {
	var req claude.Request
	if err := json.Unmarshal(body, &req); err != nil {
		return len(body) / 3
	}
	return tokens.WeightedRequestTokens(req)
}

func jsonTokens(v interface{}) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return tokens.WeightedCharTokens(string(b))
}

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}
