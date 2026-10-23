// Part of the schema package — see jsonpath.go for the package doc
// comment. Direct port of v1's recordNumericEnumFields/
// walkSchemaForNumericEnums/coerceNumericEnumArgs — a cross-cutting
// correction to a tool CALL's argument VALUES, not a transformation of
// the schema itself (kept separate from enums.go for that reason).
//
// Gemini only allows enum on STRING-typed fields, but convertEnumValuesToStrings
// (enums.go) forcing TYPE_STRING for Gemini means a numeric/boolean enum's
// value comes back as a JSON string ("1") on a function call — which
// Claude Code's own schema validator (still expecting the original
// integer/number/boolean type) rejects. A tool's schema is static for the
// life of the conversation that declared it, so this cache lives for the
// session, not the request.
//
// An audit caught that this used to be a bare package-level
// map[string]... keyed ONLY by toolName — two different sessions/tenants
// that both happen to declare a same-named tool (e.g. an MCP tool named
// "search" with different schemas per session) would silently overwrite
// each other's entry, and whichever request's RecordNumericEnumFields ran
// last would wrongly apply to every OTHER session's CoerceNumericEnumArgs
// call for that same tool name until the next overwrite. Switched to
// internal/ephemeral.Store (the same generic TTL-keyed cache
// internal/stream/caches.go and internal/toolsig already use for
// genuinely session-scoped state) keyed by sessionID+toolName instead of
// toolName alone.
package schema

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/ephemeral"
)

const (
	numericEnumStaleAfter = 2 * time.Hour
	numericEnumSweepEvery = 30 * time.Minute
)

var numericEnumFields = ephemeral.New[map[string]string](numericEnumStaleAfter, numericEnumSweepEvery)

func numericEnumKey(sessionID, toolName string) string { return sessionID + "\x00" + toolName }

// RecordNumericEnumFields scans a tool's ORIGINAL (pre-clean) input_schema
// recursively for properties with a non-string enum, so the type can be
// restored on the way back in CoerceNumericEnumArgs. Must run AFTER
// references are inlined and allOf/anyOf structures are merged, but
// BEFORE convertEnumValuesToStrings coerces them to string enums, so an
// enum defined via $ref or allOf composition is still correctly recorded.
func RecordNumericEnumFields(sessionID, toolName, inputSchemaRaw string) {
	fields := make(map[string]string)
	walkSchemaForNumericEnums(gjson.Get(inputSchemaRaw, "properties"), "", fields)
	key := numericEnumKey(sessionID, toolName)
	if len(fields) == 0 {
		numericEnumFields.Delete(key)
		return
	}
	numericEnumFields.Set(key, fields)
}

func firstNonNullEnumType(enumArr gjson.Result) string {
	for _, ev := range enumArr.Array() {
		if ev.Type == gjson.Number {
			if strings.Contains(ev.Raw, ".") {
				return "number"
			}
			return "integer"
		}
		if ev.Type == gjson.True || ev.Type == gjson.False {
			return "boolean"
		}
	}
	return ""
}

func walkSchemaForNumericEnums(props gjson.Result, prefix string, fields map[string]string) {
	if !props.IsObject() {
		return
	}
	props.ForEach(func(key, val gjson.Result) bool {
		path := key.String()
		if prefix != "" {
			path = prefix + "." + key.String()
		}
		t := val.Get("type").String()
		if orig := val.Get("x-orig-type").String(); orig != "" {
			t = orig
		} else if t == "" && val.Get("enum").IsArray() {
			// Scan for the first NON-null element rather than assuming
			// enum[0] is representative — a nullable numeric/boolean enum
			// is commonly authored as [null, 1, 2].
			if inferred := firstNonNullEnumType(val.Get("enum")); inferred != "" {
				t = inferred
			}
		}
		if val.Get("enum").IsArray() && (t == "integer" || t == "number" || t == "boolean") {
			fields[path] = t
		}
		if val.Get("properties").IsObject() {
			walkSchemaForNumericEnums(val.Get("properties"), path, fields)
		}
		if t == "array" || val.Get("items").Exists() {
			walkArrayForNumericEnums(val, path, fields)
		}
		return true
	})
}

// walkArrayForNumericEnums handles every numeric/boolean-enum shape an
// array's "items" schema can take: a flat scalar enum (marked
// "array:<type>"), a 2D array of arrays (items.items, marked
// "array2D:<type>"), and an array of objects (items.properties, marked
// per sub-field as "arrayObject:<subfield>:<type>" since the array's
// length isn't known until real call arguments arrive).
func walkArrayForNumericEnums(val gjson.Result, path string, fields map[string]string) {
	items := val.Get("items")
	itemType := items.Get("type").String()
	if orig := items.Get("x-orig-type").String(); orig != "" {
		itemType = orig
	} else if itemType == "" && items.Get("enum").IsArray() {
		if inferred := firstNonNullEnumType(items.Get("enum")); inferred != "" {
			itemType = inferred
		}
	}
	if items.Get("enum").IsArray() && (itemType == "integer" || itemType == "number" || itemType == "boolean") {
		fields[path] = "array:" + itemType
	}
	if itemType == "array" {
		innerItems := items.Get("items")
		innerType := innerItems.Get("type").String()
		if orig := innerItems.Get("x-orig-type").String(); orig != "" {
			innerType = orig
		} else if innerType == "" && innerItems.Get("enum").IsArray() {
			if inferred := firstNonNullEnumType(innerItems.Get("enum")); inferred != "" {
				innerType = inferred
			}
		}
		if innerItems.Get("enum").IsArray() && (innerType == "integer" || innerType == "number" || innerType == "boolean") {
			fields[path] = "array2D:" + innerType
		}
	}
	if items.Get("properties").IsObject() {
		walkArrayItemProperties(items.Get("properties"), path+"[]", fields)
	}
}

func walkArrayItemProperties(props gjson.Result, prefix string, fields map[string]string) {
	if !props.IsObject() {
		return
	}
	props.ForEach(func(subKey, subVal gjson.Result) bool {
		subPath := prefix + "." + subKey.String()
		subT := subVal.Get("type").String()
		if orig := subVal.Get("x-orig-type").String(); orig != "" {
			subT = orig
		} else if subT == "" && subVal.Get("enum").IsArray() {
			if inferred := firstNonNullEnumType(subVal.Get("enum")); inferred != "" {
				subT = inferred
			}
		}
		if subVal.Get("enum").IsArray() && (subT == "integer" || subT == "number" || subT == "boolean") {
			fields[subPath] = "arrayObject:" + subT
		}
		if subVal.Get("properties").IsObject() {
			walkArrayItemProperties(subVal.Get("properties"), subPath, fields)
		}
		if subT == "array" || subVal.Get("items").Exists() {
			walkArrayForNumericEnums(subVal, subPath, fields)
		}
		return true
	})
}

// CoerceNumericEnumArgs restores a function call's string-coerced enum
// fields back to their declared integer/number/boolean type before the
// args are handed to the client as a tool_use block.
func CoerceNumericEnumArgs(sessionID, toolName, argsRaw string) string {
	fields, _ := numericEnumFields.Get(numericEnumKey(sessionID, toolName))
	for field, origType := range fields {
		if field == "__scalar_arg__" {
			v := gjson.Parse(argsRaw)
			if v.Type == gjson.String {
				argsRaw = coerceScalar(argsRaw, "", origType, v.String())
				continue
			}
		}
		if itemType, ok := strings.CutPrefix(origType, "arrayObject:"); ok {
			argsRaw = coerceArrayObjectField(argsRaw, field, itemType)
			continue
		}
		if itemType, ok := strings.CutPrefix(origType, "array:"); ok {
			argsRaw = coerceArrayField(argsRaw, field, itemType)
			continue
		}
		if itemType, ok := strings.CutPrefix(origType, "array2D:"); ok {
			argsRaw = coerceArray2DField(argsRaw, field, itemType)
			continue
		}
		argsRaw = coerceScalarField(argsRaw, field, origType)
	}
	return argsRaw
}

func coerceScalar(argsRaw, path, itemType, s string) string {
	sTrimmed := strings.TrimSpace(s)
	switch itemType {
	case "integer":
		if n, err := strconv.ParseInt(sTrimmed, 10, 64); err == nil {
			if path == "" {
				return strconv.FormatInt(n, 10)
			}
			argsRaw, _ = sjson.Set(argsRaw, path, n)
		} else if f, err := strconv.ParseFloat(sTrimmed, 64); err == nil && f == float64(int64(f)) {
			if path == "" {
				return strconv.FormatInt(int64(f), 10)
			}
			argsRaw, _ = sjson.Set(argsRaw, path, int64(f))
		}
	case "number":
		if f, err := strconv.ParseFloat(sTrimmed, 64); err == nil {
			if path == "" {
				return strconv.FormatFloat(f, 'f', -1, 64)
			}
			argsRaw, _ = sjson.Set(argsRaw, path, f)
		}
	case "boolean":
		if b, err := strconv.ParseBool(sTrimmed); err == nil {
			if path == "" {
				return strconv.FormatBool(b)
			}
			argsRaw, _ = sjson.Set(argsRaw, path, b)
		}
	}
	return argsRaw
}

func coerceScalarField(argsRaw, field, origType string) string {
	v := gjson.Get(argsRaw, field)
	if v.Type != gjson.String {
		return argsRaw
	}
	return coerceScalar(argsRaw, field, origType, v.String())
}

func coerceArrayField(argsRaw, field, itemType string) string {
	arr := gjson.Get(argsRaw, field)
	if !arr.IsArray() {
		return argsRaw
	}
	for i, el := range arr.Array() {
		if el.Type != gjson.String {
			continue
		}
		argsRaw = coerceScalar(argsRaw, fmt.Sprintf("%s.%d", field, i), itemType, el.String())
	}
	return argsRaw
}

func coerceArray2DField(argsRaw, field, itemType string) string {
	rows := gjson.Get(argsRaw, field)
	if !rows.IsArray() {
		return argsRaw
	}
	for ri, row := range rows.Array() {
		if !row.IsArray() {
			continue
		}
		for ci, el := range row.Array() {
			if el.Type != gjson.String {
				continue
			}
			argsRaw = coerceScalar(argsRaw, fmt.Sprintf("%s.%d.%d", field, ri, ci), itemType, el.String())
		}
	}
	return argsRaw
}

func coerceArrayObjectField(argsRaw, field, itemType string) string {
	arrayPath, subKey, found := strings.Cut(field, "[].")
	if !found {
		return argsRaw
	}
	arr := gjson.Get(argsRaw, arrayPath)
	if !arr.IsArray() {
		return argsRaw
	}
	for i, el := range arr.Array() {
		v := el.Get(subKey)
		if v.Type != gjson.String {
			continue
		}
		argsRaw = coerceScalar(argsRaw, fmt.Sprintf("%s.%d.%s", arrayPath, i, subKey), itemType, v.String())
	}
	return argsRaw
}
