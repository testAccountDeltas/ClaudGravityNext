// Part of the schema package — see jsonpath.go for the package doc
// comment. Direct port of v1's CleanJSONSchemaForAntigravity/
// CleanJSONSchemaForGemini/cleanJSONSchema — the pipeline orchestrator
// that calls every file in this package, in the fixed order their own
// comments document a dependency on.
package schema

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// CleanJSONSchemaForAntigravity transforms a JSON schema to be compatible
// with the Antigravity API's tool-calling path. Handles unsupported
// keywords, type flattening, and schema simplification while preserving
// semantic information as description hints, and adds the "reason"
// placeholder Claude VALIDATED mode requires on a genuinely empty object
// schema.
func CleanJSONSchemaForAntigravity(sessionID, toolName, jsonStr string) string {
	if toolName != "" {
		RecordToolSchema(sessionID, toolName, jsonStr)
	}
	trimmed := strings.TrimSpace(jsonStr)
	if trimmed == "" || trimmed == "{}" || (gjson.Parse(trimmed).IsObject() && len(gjson.Parse(trimmed).Map()) == 0) {
		return `{"type":"object","properties":{"reason":{"type":"string","description":"Placeholder"}},"required":["reason"]}`
	}
	rootType := gjson.Get(jsonStr, "type").String()
	if rootType != "" && !strings.EqualFold(rootType, "object") {
		jsonStr = fmt.Sprintf(`{"type":"object","properties":{"__scalar_arg__":%s},"required":["__scalar_arg__"]}`, jsonStr)
	}
	cleaned, nestedOpenDictFields := cleanJSONSchema(sessionID, toolName, jsonStr, true)
	if !strings.EqualFold(gjson.Get(cleaned, "type").String(), "object") {
		cleaned = fmt.Sprintf(`{"type":"object","properties":{"__scalar_arg__":%s},"required":["__scalar_arg__"]}`, cleaned)
	}
	setOpenDictArgFields(sessionID, toolName, nestedOpenDictFields)
	return cleaned
}

// CleanJSONSchemaForGemini transforms a JSON schema to be compatible with
// Gemini tool calling / Structured Outputs' responseSchema. Removes
// unsupported keywords and simplifies schemas, without adding empty-
// schema placeholders.
func CleanJSONSchemaForGemini(jsonStr string) string {
	cleaned, _ := cleanJSONSchema("", "", jsonStr, false)
	return cleaned
}

// cleanJSONSchema performs the core cleaning operations on the JSON
// schema. Returns the cleaned schema and, when addPlaceholder is set, the
// top-level argument field names addEmptySchemaPlaceholder had to patch
// because they were free-form dicts rather than genuinely empty schemas.
func cleanJSONSchema(sessionID, toolName, jsonStr string, addPlaceholder bool) (string, []string) {
	// A node with additionalProperties set to a SCHEMA (not just `false`)
	// is a free-form dict by design (Bash's "env", TaskCreate's
	// "metadata") and legitimately has no fixed "properties" of its own.
	// Phase 3 below strips additionalProperties entirely as unsupported,
	// which otherwise leaves nothing behind to distinguish that node from
	// a genuinely empty schema (triggering Phase 4's "reason" placeholder
	// injection the same way), so record which nodes are open dicts now,
	// before anything else runs.
	openDictPaths := make(map[string]bool)
	for _, p := range findPaths(jsonStr, "additionalProperties") {
		parentPath := trimSuffix(p, ".additionalProperties")
		if isPropertyDefinition(parentPath) {
			continue
		}
		if gjson.Get(jsonStr, p).Type != gjson.False {
			dictPath := parentPath
			openDictPaths[dictPath] = true
			// A free-form dict declared only as {"additionalProperties":
			// {"type": "string"}} (no "type" of its own — common in
			// Pydantic/TypeScript-generated schemas) loses its
			// additionalProperties in Phase 3 with nothing left behind to
			// mark it as an object; Phase 4 only ever looks at paths where
			// "type" ALREADY exists, so this node would reach Gemini as a
			// bare "{}" ("Schema cannot be empty"). Force "type":"object"
			// now so Phase 4 sees it.
			if !gjson.Get(jsonStr, joinPath(dictPath, "type")).Exists() {
				jsonStr, _ = sjson.Set(jsonStr, joinPath(dictPath, "type"), "object")
			}
		}
	}
	// GAP-S9: a node declared ONLY via "patternProperties" (no fixed
	// "properties" of its own — another free-form-dict shape, just keyed
	// by a regex instead of "additionalProperties") is exactly as much an
	// open dict as the additionalProperties case above, needing the same
	// "type":"object" forced in before Phase 3's removeUnsupportedKeywords
	// strips patternProperties out from under it. Without this, such a
	// node reaches Phase 4 with no "type" key at all — findPaths(jsonStr,
	// "type") never even visits it, so it skips straight past the
	// placeholder-then-strip treatment every other empty-object node gets
	// and reaches Gemini as a bare {} ("Schema cannot be empty"), strictly
	// worse than getting the placeholder would have been.
	for _, p := range findPaths(jsonStr, "patternProperties") {
		parentPath := trimSuffix(p, ".patternProperties")
		if isPropertyDefinition(parentPath) {
			continue
		}
		dictPath := parentPath
		openDictPaths[dictPath] = true
		if !gjson.Get(jsonStr, joinPath(dictPath, "type")).Exists() {
			jsonStr, _ = sjson.Set(jsonStr, joinPath(dictPath, "type"), "object")
		}
	}

	// A root schema that implies "object" by having "properties" (valid
	// per JSON Schema — explicit "type" is optional when properties are
	// present) must get an explicit "type":"OBJECT" forced in now, before
	// anything else runs — otherwise it can reach Gemini as
	// parameters:{"properties":...} with no type, which Gemini's upstream
	// rejects with a 400.
	// GAP-CGN-05: If schema is empty or lacks "type" (and has no properties/items),
	// treat as object so placeholder or proper object schema is produced.
	trimmed := strings.TrimSpace(jsonStr)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		jsonStr = `{"type":"object"}`
	} else if gjson.Get(jsonStr, "properties").Exists() && !gjson.Get(jsonStr, "type").Exists() {
		jsonStr, _ = sjson.Set(jsonStr, "type", "object")
	} else if gjson.Get(jsonStr, "items").Exists() && !gjson.Get(jsonStr, "type").Exists() {
		jsonStr, _ = sjson.Set(jsonStr, "type", "array")
	} else if gjson.Get(jsonStr, "enum").Exists() && !gjson.Get(jsonStr, "type").Exists() {
		// Root enum without type: let sanitizeEnums infer scalar type (string/integer/boolean)
	} else if !gjson.Get(jsonStr, "type").Exists() {
		jsonStr, _ = sjson.Set(jsonStr, "type", "object")
	}

	// GAP-JS-09: Gemini requires function declaration parameters to have type: OBJECT.
	// If a tool input schema has a bare scalar root (type != "object" and no "properties"),
	// wrap it symmetrically into {"type":"object","properties":{"__scalar_arg__":<scalar_schema>},"required":["__scalar_arg__"]}.
	if addPlaceholder {
		jsonStr = wrapBareScalarRoot(jsonStr)
	}

	// Phase 1: Convert references and inline structures.
	jsonStr = convertRefsToHints(jsonStr)
	jsonStr = convertConstToEnum(jsonStr)
	jsonStr = convertPrefixItemsToItems(jsonStr)
	jsonStr = convertDraft7TuplesToItems(jsonStr)

	// Phase 2: Flatten complex structures.
	jsonStr = mergeAllOf(jsonStr)
	jsonStr = flattenAnyOfOneOf(jsonStr, addPlaceholder)
	jsonStr = mergeAllOf(jsonStr)
	jsonStr = flattenTypeArrays(jsonStr, addPlaceholder)
	if addPlaceholder {
		jsonStr = wrapBareScalarRoot(jsonStr)
	}

	// Record numeric/boolean enums AFTER references are inlined and
	// allOf/anyOf structures are merged, but BEFORE convertEnumValuesToStrings
	// coerces them to string enums for Gemini OpenAPI 3.0 — this ensures
	// enums defined via $ref or allOf composition are correctly preserved
	// for coercion upon response.
	if toolName != "" {
		RecordNumericEnumFields(sessionID, toolName, jsonStr)
	}

	// Phase 2: Schema normalization and sanitization.
	// Sanitize enums unconditionally: strip null literals, drop empty enums,
	// and infer types. Only coerce to strings if addPlaceholder == true (tool declarations).
	jsonStr = sanitizeEnums(jsonStr, addPlaceholder)
	jsonStr = addEnumHints(jsonStr)
	jsonStr = addAdditionalPropertiesHints(jsonStr)
	jsonStr = moveConstraintsToDescription(jsonStr)

	// Phase 3: Cleanup.
	jsonStr = removeUnsupportedKeywords(jsonStr)
	if !addPlaceholder {
		// "nullable" is only ever stripped for Structured Outputs'
		// responseSchema (addPlaceholder==false) — Gemini's own
		// generationConfig.responseSchema protobuf explicitly supports
		// "nullable" as field 6, and tool declarations (addPlaceholder==
		// true) already correctly preserve it; removing it here would
		// lose the one signal telling Gemini a Structured Outputs field
		// may legitimately be null.
		jsonStr = removePlaceholderFields(jsonStr)
		jsonStr = fixEmptyObjectSchemasForResponseSchema(jsonStr)
	}
	jsonStr = cleanupRequiredFields(jsonStr, addPlaceholder)
	// GAP-CGN-D2-01 (GAP-D2-07): addMissingArrayItems MUST run before addMissingPropertyTypes
	// so newly synthesized items:{} receives an object/string type rather than staying untyped.
	jsonStr = addMissingArrayItems(jsonStr, addPlaceholder)
	jsonStr = addMissingPropertyTypes(jsonStr)
	jsonStr = stripKeysNotInAllowlist(jsonStr)

	// Phase 4: Add placeholder for empty object schemas (Claude VALIDATED
	// mode requirement).
	var nestedOpenDictFields []string
	if addPlaceholder {
		jsonStr, nestedOpenDictFields = addEmptySchemaPlaceholder(jsonStr, openDictPaths)

		// GAP-JS-09 / GAP-CGN-D2-02: Gemini requires parameters to be strictly of type OBJECT.
		// If a custom tool defines an input_schema with a scalar/array root, wrap it in an object.
		jsonStr = wrapBareScalarRoot(jsonStr)
	}

	return jsonStr, nestedOpenDictFields
}

func wrapBareScalarRoot(jsonStr string) string {
	schemaType := gjson.Get(jsonStr, "type").String()
	if schemaType != "" && !strings.EqualFold(schemaType, "object") && !gjson.Get(jsonStr, "properties").Exists() {
		var wrapped string
		wrapped, _ = sjson.SetRaw(`{"type":"object"}`, "properties.__scalar_arg__", jsonStr)
		wrapped, _ = sjson.Set(wrapped, "required", []string{"__scalar_arg__"})
		return wrapped
	}
	return jsonStr
}
