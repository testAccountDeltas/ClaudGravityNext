// Part of the schema package — see jsonpath.go for the package doc
// comment. Direct port of v1's convertConstToEnum/convertEnumValuesToStrings/
// addEnumHints.
package schema

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// convertConstToEnum rewrites a "const" keyword into a single-element
// "enum" (Gemini has no const concept), inferring and setting "type" too
// if the parent doesn't already declare one — immediately, not left for
// convertEnumValuesToStrings to fill in later, since anyof.go's
// flattenAnyOfOneOf (which runs between the two) needs to recognize a
// const-derived branch as a typed enum right away to merge it correctly
// with sibling branches instead of mislabeling it nullable.
func convertConstToEnum(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "const") {
		val := gjson.Get(jsonStr, p)
		if !val.Exists() {
			continue
		}
		parentPath := trimSuffix(p, ".const")
		if isPropertyDefinition(parentPath) {
			continue
		}
		// GAP-S5: a root-level "const" (parentPath=="") must produce the
		// path "enum"/"type", not ".enum"/".type" — plain string
		// concatenation left a leading dot for exactly that case, which
		// sjson.Set then wrote as a literal top-level key named ".enum"
		// instead of "enum", so the real "enum" key sjson/convertEnum...
		// later look for never existed. joinPath already handles the
		// base=="" case correctly (used everywhere else in this package).
		enumPath := joinPath(parentPath, "enum")
		if !gjson.Get(jsonStr, enumPath).Exists() {
			jsonStr, _ = sjson.Set(jsonStr, enumPath, []interface{}{val.Value()})
		}
		typePath := joinPath(parentPath, "type")
		if gjson.Get(jsonStr, typePath).Exists() {
			continue
		}
		var inferredType string
		switch val.Type {
		case gjson.String:
			inferredType = "string"
		case gjson.Number:
			inferredType = "number"
			if val.Num == float64(int64(val.Num)) {
				inferredType = "integer"
			}
		case gjson.True, gjson.False:
			inferredType = "boolean"
		}
		if inferredType != "" {
			jsonStr, _ = sjson.Set(jsonStr, typePath, inferredType)
		}
	}
	return jsonStr
}

// sanitizeEnums sanitizes enums for Gemini Protobuf compliance.
// It removes literal `null` items and marks the schema as "nullable: true",
// deletes empty enums (enum: []) or purely-null enums (enum: [null]),
// and infers and sets "type" if missing based on enum elements.
// If coerceToString is true (tool declarations in OpenAPI 3.0), it coerces all values
// to strings and forces type to "string". If false (Structured Outputs responseSchema),
// it preserves native numeric/boolean/string enums and sets the appropriate type.
func sanitizeEnums(jsonStr string, coerceToString bool) string {
	for _, p := range findPaths(jsonStr, "enum") {
		arr := gjson.Get(jsonStr, p)
		if !arr.IsArray() {
			continue
		}

		var nonNullVals []gjson.Result
		var stringVals []string
		hasNull := false
		inferredType := ""

		for _, item := range arr.Array() {
			if item.Type == gjson.Null {
				hasNull = true
				continue
			}
			nonNullVals = append(nonNullVals, item)
			stringVals = append(stringVals, item.String())
			if inferredType == "" {
				switch item.Type {
				case gjson.String:
					inferredType = "string"
				case gjson.Number:
					inferredType = "number"
					if item.Num == float64(int64(item.Num)) {
						inferredType = "integer"
					}
				case gjson.True, gjson.False:
					inferredType = "boolean"
				}
			}
		}

		parentPath := trimSuffix(p, ".enum")
		if isPropertyDefinition(parentPath) {
			continue
		}
		if len(nonNullVals) == 0 {
			// GAP-SC2: Purely-null or empty enum is invalid in Gemini protobuf.
			// Delete enum and set type="string" with nullable=true.
			jsonStr, _ = sjson.Delete(jsonStr, p)
			jsonStr, _ = sjson.Set(jsonStr, joinPath(parentPath, "type"), "string")
			jsonStr, _ = sjson.Set(jsonStr, joinPath(parentPath, "nullable"), true)
			continue
		}

		if coerceToString {
			jsonStr, _ = sjson.Set(jsonStr, p, stringVals)
			jsonStr, _ = sjson.Set(jsonStr, joinPath(parentPath, "type"), "string")
		} else {
			// In Structured Outputs: preserve native values if null was stripped
			if hasNull {
				var rawVals []interface{}
				for _, it := range nonNullVals {
					rawVals = append(rawVals, it.Value())
				}
				jsonStr, _ = sjson.Set(jsonStr, p, rawVals)
			}
			typePath := joinPath(parentPath, "type")
			if !gjson.Get(jsonStr, typePath).Exists() && inferredType != "" {
				jsonStr, _ = sjson.Set(jsonStr, typePath, inferredType)
			}
		}

		if hasNull {
			jsonStr, _ = sjson.Set(jsonStr, joinPath(parentPath, "nullable"), true)
		}
	}
	return jsonStr
}

// convertEnumValuesToStrings ensures all enum values are strings and the
// schema type is set to "string" — Gemini API requires enum values to be
// of type string, not numbers or booleans.
func convertEnumValuesToStrings(jsonStr string) string {
	return sanitizeEnums(jsonStr, true)
}

// addEnumHints appends an "Allowed: a, b, c" description hint for any
// small (2-10 value) enum, since Gemini's own protobuf Schema carries no
// human-readable enum documentation of its own.
func addEnumHints(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "enum") {
		parentPath := trimSuffix(p, ".enum")
		if isPropertyDefinition(parentPath) {
			continue
		}
		arr := gjson.Get(jsonStr, p)
		if !arr.IsArray() {
			continue
		}
		items := arr.Array()
		if len(items) <= 1 || len(items) > 10 {
			continue
		}

		var vals []string
		for _, item := range items {
			vals = append(vals, item.String())
		}
		jsonStr = appendHint(jsonStr, parentPath, "Allowed: "+strings.Join(vals, ", "))
	}
	return jsonStr
}
