// Part of the schema package — see jsonpath.go for the package doc
// comment. Direct port of v1's convertDraft7TuplesToItems/
// convertPrefixItemsToItems — the one file the migration spec calls out
// (Part 3 p.3) as carrying a documented compromise with no positional
// validation: Gemini's protobuf Schema has no tuple concept at all, so a
// multi-element tuple can only approximate as a {"oneOf": [...]} of its
// element schemas (collapsed further by anyof.go's flattenAnyOfOneOf,
// which runs right after this in the pipeline) — strictly closer to the
// original intent than the alternative of discarding every element past
// the first, but the model is no longer told WHICH position must match
// WHICH schema.
package schema

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// convertDraft7TuplesToItems rewrites Draft-7-style tuple validation
// ("items": [schemaA, schemaB, ...], the array form that predates
// "prefixItems") into the same {"oneOf": [...]} shape
// convertPrefixItemsToItems produces for its Draft 2020-12 equivalent.
// Must run in Phase 1, before addMissingArrayItems (required.go) would
// otherwise take just the first element's schema and discard the rest
// into a lossy "Tuple items: [...]" text hint.
func convertDraft7TuplesToItems(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "items") {
		arr := gjson.Get(jsonStr, p)
		if !arr.IsArray() || len(arr.Array()) == 0 {
			continue
		}
		items := arr.Array()
		if len(items) == 1 {
			jsonStr, _ = sjson.SetRaw(jsonStr, p, items[0].Raw)
			continue
		}
		oneOf := "["
		for i, it := range items {
			if i > 0 {
				oneOf += ","
			}
			oneOf += it.Raw
		}
		oneOf += "]"
		jsonStr, _ = sjson.SetRaw(jsonStr, p, `{"oneOf":`+oneOf+`}`)
	}
	return jsonStr
}

// convertPrefixItemsToItems rewrites Draft 2020-12 tuple validation
// ("prefixItems": [schemaA, schemaB, ...]) into an "items" schema before
// "prefixItems" is stripped as an unsupported keyword later in the
// pipeline — without this, the tuple's element schemas would simply be
// discarded (prefixItems deleted outright, "items" never existed on these
// schemas to begin with, and addMissingArrayItems fills that gap with a
// bare {}).
func convertPrefixItemsToItems(jsonStr string) string {
	for _, p := range findPaths(jsonStr, "prefixItems") {
		arr := gjson.Get(jsonStr, p)
		if !arr.IsArray() || len(arr.Array()) == 0 {
			continue
		}
		parentPath := trimSuffix(p, ".prefixItems")
		itemsPath := joinPath(parentPath, "items")
		// gjson's Exists() reports true for ANY JSON value at the path,
		// including the literal boolean `false` — Draft 2020-12 tuples
		// commonly pair "prefixItems" with "items": false (meaning "no
		// additional items beyond the tuple," not "items has a real
		// schema already"). Only a non-false existing value blocks the
		// conversion.
		existingItems := gjson.Get(jsonStr, itemsPath)
		if existingItems.Exists() && existingItems.Type != gjson.False {
			continue
		}
		items := arr.Array()
		if len(items) == 1 {
			jsonStr, _ = sjson.SetRaw(jsonStr, itemsPath, items[0].Raw)
			continue
		}
		oneOf := "["
		for i, it := range items {
			if i > 0 {
				oneOf += ","
			}
			oneOf += it.Raw
		}
		oneOf += "]"
		jsonStr, _ = sjson.SetRaw(jsonStr, itemsPath, `{"oneOf":`+oneOf+`}`)
	}
	return jsonStr
}
