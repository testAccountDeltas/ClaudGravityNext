// Part of the schema package — see jsonpath.go for the package doc
// comment. Remembers each tool's ORIGINAL (pre-clean) input_schema per
// session so a generated call's arguments can be conformed to what the
// client actually declared (types, nulls, enums, required) on the way
// back — the cleaned schema Gemini sees is lossy by design (anyOf
// flattened, bounds dropped), the original is the authority.
package schema

import (
	"strings"

	"claudgravity/internal/ephemeral"
	"claudgravity/internal/logx"
	"claudgravity/internal/toolargs"
)

var declaredToolSchemas = ephemeral.New[string](numericEnumStaleAfter, numericEnumSweepEvery)

// RecordToolSchema stores toolName's original input_schema for the session.
func RecordToolSchema(sessionID, toolName, inputSchemaRaw string) {
	key := numericEnumKey(sessionID, toolName)
	if strings.TrimSpace(inputSchemaRaw) == "" {
		declaredToolSchemas.Delete(key)
		return
	}
	declaredToolSchemas.Set(key, inputSchemaRaw)
}

// ConformToolArgs coerces argsRaw to the schema recorded for the tool:
// string fields stay strings, integer/number/boolean fields accept
// strings, a JSON-encoded string becomes the array/object branch of a
// union, nulls survive only where the schema allows them, near-miss enum
// values are repaired when unambiguous. Problems it cannot repair
// (missing required argument, value outside an enum) are logged and the
// call is left as the model produced it. Without a recorded schema only
// top-level nulls are dropped.
func ConformToolArgs(sessionID, toolName, argsRaw string) string {
	raw, ok := declaredToolSchemas.Get(numericEnumKey(sessionID, toolName))
	if !ok {
		logx.LogGateway("[ToolArgs] call to %q matches no tool declared in this session; passing through", toolName)
		return toolargs.PruneTopLevelNulls(argsRaw)
	}
	out, issues := toolargs.ConformToSchema(argsRaw, raw)
	if len(issues) > 0 {
		logx.LogGateway("[ToolArgs] %s: %s", toolName, strings.Join(issues, "; "))
	}
	return out
}
