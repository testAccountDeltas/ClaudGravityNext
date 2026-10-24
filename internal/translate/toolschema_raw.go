// Part of the translate package. Support for the tenant option
// use_parameters_json_schema: the tool's own JSON Schema goes to Gemini as
// functionDeclarations[].parametersJsonSchema instead of passing through the
// schema cleaner.
package translate

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/schema"
)

// rawParametersSchema returns the tool's input_schema unchanged, and records
// it so a generated call's arguments are still conformed to the declared
// schema on the way back. An empty or null schema returns "" so the caller's
// existing empty-schema placeholder handling applies.
func rawParametersSchema(sessionID, toolName, rawSchema string) string {
	if toolName != "" {
		schema.RecordToolSchema(sessionID, toolName, rawSchema)
	}
	trimmed := strings.TrimSpace(rawSchema)
	if trimmed == "" || trimmed == "null" || !gjson.Valid(trimmed) {
		return ""
	}
	return trimmed
}

// wrapScalarRoot nests a non-object root schema under a single required
// "__scalar_arg__" property (the response path unwraps it). Definitions
// move to the new root so "#/$defs/..." and "#/definitions/..." references
// keep resolving.
func wrapScalarRoot(schemaRaw string) string {
	inner := schemaRaw
	wrapper := `{"type":"object","properties":{},"required":["__scalar_arg__"]}`
	for _, defs := range []string{"$defs", "definitions"} {
		if d := gjson.Get(inner, defs); d.Exists() {
			wrapper, _ = sjson.SetRaw(wrapper, defs, d.Raw)
			inner, _ = sjson.Delete(inner, defs)
		}
	}
	wrapper, _ = sjson.SetRaw(wrapper, "properties.__scalar_arg__", inner)
	return wrapper
}

// hasCompositionRoot reports a root schema built from combinators or a
// reference rather than from "properties".
func hasCompositionRoot(schemaRaw string) bool {
	for _, k := range []string{"anyOf", "oneOf", "allOf", "$ref", "not"} {
		if gjson.Get(schemaRaw, k).Exists() {
			return true
		}
	}
	return false
}
