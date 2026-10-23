// Part of the schema package — see jsonpath.go for the package doc
// comment. Direct port of v1's cleanupRequiredFields/flattenTypeArrays,
// plus addMissingPropertyTypes/addMissingArrayItems (not explicitly named
// by the migration spec's file list — grouped here since all four are the
// same "Phase 3 cleanup: fill in whatever Gemini's protobuf Schema would
// otherwise reject as missing" concern).
package schema

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// cleanupRequiredFields drops any "required" entry that doesn't name a
// real property (or isn't even an array). If stripNullable is true (tool
// declarations, where Gemini treats nullable as optional), properties with
// "nullable": true are also stripped from "required" to avoid validation failures.
func cleanupRequiredFields(jsonStr string, stripNullable bool) string {
	for _, p := range findPaths(jsonStr, "required") {
		parentPath := trimSuffix(p, ".required")
		propsPath := joinPath(parentPath, "properties")

		req := gjson.Get(jsonStr, p)
		props := gjson.Get(jsonStr, propsPath)
		if !req.IsArray() {
			jsonStr, _ = sjson.Delete(jsonStr, p)
			continue
		}
		if !props.IsObject() {
			jsonStr, _ = sjson.Delete(jsonStr, p)
			continue
		}

		var valid []string
		for _, r := range req.Array() {
			key := r.String()
			prop := props.Get(escapeGJSONPathKey(key))
			if prop.Exists() {
				if stripNullable && prop.Get("nullable").Bool() {
					continue
				}
				valid = append(valid, key)
			}
		}

		if len(valid) == 0 {
			jsonStr, _ = sjson.Delete(jsonStr, p)
		} else if len(valid) != len(req.Array()) {
			jsonStr, _ = sjson.Set(jsonStr, p, valid)
		}
	}
	return jsonStr
}

// flattenTypeArrays collapses a JSON Schema type union ("type":
// ["string","null"], or a bare "type":"null") into the single scalar type
// Gemini's protobuf Schema actually recognizes (STRING/NUMBER/INTEGER/
// BOOLEAN/ARRAY/OBJECT, never "null"), setting "nullable": true when
// "null" was one of the options. addPlaceholder gates whether a now-
// nullable field is also stripped from its parent's "required" array:
// correct for a tool-declaration schema (Gemini treats nullable as
// optional there), but WRONG for Structured Outputs' responseSchema,
// where "required" (key must be present) is independent of "nullable"
// (value may be null) — Pydantic/Zod client validators enforce exactly
// that distinction.
func flattenTypeArrays(jsonStr string, addPlaceholder bool) string {
	paths := findPaths(jsonStr, "type")
	sortByDepth(paths)

	nullableFields := make(map[string][]string)

	for _, p := range paths {
		res := gjson.Get(jsonStr, p)
		if res.Type == gjson.String && res.String() == "null" {
			parentPath := trimSuffix(p, ".type")
			jsonStr, _ = sjson.Set(jsonStr, p, "string")
			jsonStr, _ = sjson.Set(jsonStr, joinPath(parentPath, "nullable"), true)
			continue
		}
		if !res.IsArray() || len(res.Array()) == 0 {
			continue
		}

		hasNull := false
		var nonNullTypes []string
		for _, item := range res.Array() {
			s := item.String()
			if s == "null" {
				hasNull = true
			} else if s != "" {
				nonNullTypes = append(nonNullTypes, s)
			}
		}

		firstType := "string"
		if len(nonNullTypes) > 0 {
			firstType = nonNullTypes[0]
		}
		// Prefer whichever declared type actually matches the schema's own
		// structural keywords (properties/items) over the union's
		// declaration order — Gemini rejects a string-typed field carrying
		// "properties"/"items".
		parentPathPeek := trimSuffix(p, ".type")
		if gjson.Get(jsonStr, joinPath(parentPathPeek, "properties")).IsObject() && contains(nonNullTypes, "object") {
			firstType = "object"
		} else if gjson.Get(jsonStr, joinPath(parentPathPeek, "items")).Exists() && contains(nonNullTypes, "array") {
			firstType = "array"
		}

		jsonStr, _ = sjson.Set(jsonStr, p, firstType)

		parentPath := trimSuffix(p, ".type")
		if len(nonNullTypes) > 1 {
			hint := "Accepts: " + strings.Join(nonNullTypes, " | ")
			jsonStr = appendHint(jsonStr, parentPath, hint)
		}

		if hasNull {
			jsonStr, _ = sjson.Set(jsonStr, joinPath(parentPath, "nullable"), true)
			parts := splitGJSONPath(p)
			if len(parts) >= 3 && parts[len(parts)-3] == "properties" {
				fieldNameEscaped := parts[len(parts)-2]
				fieldName := unescapeGJSONPathKey(fieldNameEscaped)
				objectPath := strings.Join(parts[:len(parts)-3], ".")
				nullableFields[objectPath] = append(nullableFields[objectPath], fieldName)

				propPath := joinPath(objectPath, "properties."+fieldNameEscaped)
				jsonStr = appendHint(jsonStr, propPath, "(nullable)")
			}
		}
	}

	if addPlaceholder {
		for objectPath, fields := range nullableFields {
			reqPath := joinPath(objectPath, "required")
			req := gjson.Get(jsonStr, reqPath)
			if !req.IsArray() {
				continue
			}

			var filtered []string
			for _, r := range req.Array() {
				if !contains(fields, r.String()) {
					filtered = append(filtered, r.String())
				}
			}

			if len(filtered) == 0 {
				jsonStr, _ = sjson.Delete(jsonStr, reqPath)
			} else {
				jsonStr, _ = sjson.Set(jsonStr, reqPath, filtered)
			}
		}
	}
	return jsonStr
}

// addMissingPropertyTypes gives every object property (and every array's
// "items" schema) that declares no type of its own a default one — by
// this point in the pipeline (after mergeAllOf/flattenAnyOfOneOf/
// convertConstToEnum), "properties"/"items"/"enum"/a surviving composition
// keyword already implies a type; only genuinely bare schemas are
// affected. Gemini's protobuf Schema validator rejects a property with no
// type at all.
func addMissingPropertyTypes(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "properties") {
		propsVal := gjson.Get(jsonStr, p)
		if !propsVal.IsObject() {
			continue
		}
		propsVal.ForEach(func(k, v gjson.Result) bool {
			if !v.IsObject() {
				return true
			}
			if v.Get("type").Exists() || v.Get("enum").Exists() ||
				v.Get("anyOf").Exists() || v.Get("oneOf").Exists() || v.Get("allOf").Exists() {
				return true
			}
			propPath := joinPath(p, escapeGJSONPathKey(k.String()))
			switch {
			case v.Get("properties").Exists():
				jsonStr, _ = sjson.Set(jsonStr, joinPath(propPath, "type"), "object")
			case v.Get("items").Exists():
				jsonStr, _ = sjson.Set(jsonStr, joinPath(propPath, "type"), "array")
			default:
				jsonStr, _ = sjson.Set(jsonStr, joinPath(propPath, "type"), "string")
			}
			return true
		})
	}
	// An array's own "items" schema (items: {"properties": {...}} with no
	// explicit "type" on items itself) needs the same fill-in — scanned
	// separately since it's one level shallower than the property loop
	// above.
	for _, p := range findPaths(jsonStr, "items") {
		v := gjson.Get(jsonStr, p)
		if !v.IsObject() || v.Get("type").Exists() || v.Get("enum").Exists() ||
			v.Get("anyOf").Exists() || v.Get("oneOf").Exists() || v.Get("allOf").Exists() {
			continue
		}
		switch {
		case v.Get("properties").Exists():
			jsonStr, _ = sjson.Set(jsonStr, joinPath(p, "type"), "object")
		case v.Get("items").Exists():
			// A 2D array's outer items (describing each row, itself an
			// array: items.items) must type as "array", not the "string"
			// default — a string type can't carry its own "items" field.
			jsonStr, _ = sjson.Set(jsonStr, joinPath(p, "type"), "array")
		default:
			jsonStr, _ = sjson.Set(jsonStr, joinPath(p, "type"), "string")
		}
	}
	return jsonStr
}

// addMissingArrayItems gives every array schema that declares no "items"
// an items schema. Antigravity only takes the protobuf `parameters` field,
// and that schema requires items on an array — a bare {"type":"array"}
// fails the whole request with "...parameters.properties[x].items:
// missing field." JSON Schema allows the bare form (meaning "any
// element"), and MCP servers emit it routinely; for a tool declaration
// bound for Antigravity (forTool==true), {} preserves that meaning (unlike
// {"type":"string"}, which would wrongly declare an array of objects to
// be an array of strings — both {} and that alternative were measured to
// be accepted by Antigravity's Gemini and Claude models, {} is
// semantically correct there).
//
// GAP-S3: a schema bound for the public Gemini API's own grammar compiler
// instead (forTool==false — this is the Structured Outputs responseSchema
// path, CleanJSONSchemaForGemini, which unlike every tool declaration can
// legitimately leave Antigravity's account pool and reach the public API
// directly) rejects a bare {} items schema outright with "items should be
// non-empty for ARRAY type" — a non-empty default (a permissive
// {"type":"string"}) is the safer fallback there, at the cost of losing
// JSON Schema's "any element" meaning for the rare array with no declared
// element type.
//
// Runs after flattenTypeArrays, so a ["array","null"] list is already a
// plain "array" by now. Request contents (as opposed to tool declarations)
// are left alone: a function call replayed there can legitimately hold
// "type": "array" as data, not schema.
func addMissingArrayItems(jsonStr string, forTool bool) string {
	for _, p := range findPaths(jsonStr, "type") {
		if strings.HasPrefix(p, "contents.") || strings.HasPrefix(p, "request.contents.") {
			continue
		}
		parentPath := trimSuffix(p, ".type")
		if isPropertyDefinition(parentPath) {
			continue
		}
		declared := gjson.Get(jsonStr, p)
		if declared.Type != gjson.String || !strings.EqualFold(declared.String(), "array") {
			continue
		}
		itemsPath := joinPath(parentPath, "items")
		itemsRes := gjson.Get(jsonStr, itemsPath)
		if itemsRes.Exists() {
			if itemsRes.IsArray() {
				// Draft 7 tuple syntax: "items": [{"type":"string"}, ...] —
				// OpenAPI 3.0 requires "items" to be a single Schema object.
				firstItem := "{}"
				if len(itemsRes.Array()) > 0 {
					firstItem = itemsRes.Array()[0].Raw
				}
				jsonStr = setRawAt(jsonStr, itemsPath, firstItem)
				jsonStr = appendHint(jsonStr, parentPath, "Tuple items: "+itemsRes.Raw)
			}
			continue
		}
		defaultItems := "{}"
		if !forTool {
			defaultItems = `{"type":"string"}`
		}
		jsonStr = setRawAt(jsonStr, itemsPath, defaultItems)
	}
	return jsonStr
}
