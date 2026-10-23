// Part of the schema package — see jsonpath.go for the package doc
// comment. Direct port of v1's flattenAnyOfOneOf — split from
// mergeObjectBranches/selectBest (anyof_merge.go) purely to stay under
// the 400-line ceiling; the migration spec names all three for one file,
// noted as a found-by-doing deviation in ROADMAP_PROGRESS.md.
package schema

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// flattenAnyOfOneOf collapses an "anyOf"/"oneOf" array into its parent
// node. When more than one branch is a genuine object (a discriminated
// union, e.g. action:"create" vs action:"delete"), every branch's
// properties/required are merged (mergeObjectBranches) instead of keeping
// only the first — selectBest's own scoring always ties on "object" and
// never lets a later branch win, so without this merge every action past
// the first would be unreachable. A shared enum across every non-null
// branch is unioned and deduped; a sibling free scalar branch (any value
// of the type is valid, no enum of its own) strips the enum entirely
// instead of leaving a leftover restriction that contradicts it. A "null"
// branch marks the result nullable (and, for a tool-declaration schema,
// strips the field from its parent's "required") without being able to
// outrank — or get confused with — a genuinely typed branch.
func flattenAnyOfOneOf(jsonStr string, addPlaceholder bool) string {
	for _, key := range []string{"anyOf", "oneOf"} {
		paths := findPaths(jsonStr, key)
		sortByDepth(paths)

		for _, p := range paths {
			arr := gjson.Get(jsonStr, p)
			if !arr.IsArray() || len(arr.Array()) == 0 {
				// An audit caught that an EMPTY anyOf/oneOf ([]) took this
				// branch and left the key untouched — unlike allOf's
				// equivalent empty-array case (allof.go), which falls
				// through its own zero-iteration loop to an unconditional
				// delete. An empty anyOf/oneOf has nothing to flatten but
				// still needs removing, or it leaks through to Gemini's
				// Protobuf schema validator as an unsupported field.
				if arr.Exists() {
					jsonStr, _ = sjson.Delete(jsonStr, p)
				}
				continue
			}

			parentPath := trimSuffix(p, "."+key)
			parentDesc := gjson.Get(jsonStr, descriptionPath(parentPath)).String()

			items := arr.Array()
			bestIdx, allTypes := selectBest(items)
			selected := items[bestIdx].Raw
			if !gjson.Get(selected, "type").Exists() {
				if gjson.Get(selected, "properties").Exists() {
					selected, _ = sjson.Set(selected, "type", "object")
				} else if gjson.Get(selected, "items").Exists() {
					selected, _ = sjson.Set(selected, "type", "array")
				}
			}

			objectBranches := make([]gjson.Result, 0, len(items))
			for _, itm := range items {
				t := itm.Get("type").String()
				if t == "object" || (t == "" && itm.Get("properties").Exists()) {
					objectBranches = append(objectBranches, itm)
				}
			}
			if len(objectBranches) > 1 {
				selected = mergeObjectBranches(objectBranches)
			}

			selected = mergeBranchEnums(selected, items)

			if parentDesc != "" {
				selected = mergeDescriptionRaw(selected, parentDesc)
			}

			dedupedTypes := dedupeStrings(allTypes)
			if len(dedupedTypes) > 1 {
				hint := "Accepts: " + strings.Join(dedupedTypes, " | ")
				selected = appendHintRaw(selected, hint)
			}
			if contains(allTypes, "null") {
				selected, _ = sjson.Set(selected, "nullable", true)
				if addPlaceholder {
					stripFieldFromParentRequired(&jsonStr, parentPath)
				}
			}

			jsonStr = spliceSelectedIntoParent(jsonStr, parentPath, selected, p)
		}
	}
	return jsonStr
}

func dedupeStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// mergeBranchEnums implements flattenAnyOfOneOf's own enum-merge rule: if
// every non-null branch declares an enum, merge+dedupe all of their values
// into selected's own "enum" (convertEnumValuesToStrings, which runs right
// after this in the pipeline, stringifies every value regardless, so
// merging across differently-typed scalar branches is safe here); if any
// sibling is a genuine free scalar with no enum of its own, strip "enum"
// from selected entirely instead, since a free scalar's whole meaning
// ("any value of this type is valid") directly contradicts a leftover
// enum restriction.
func mergeBranchEnums(selected string, items []gjson.Result) string {
	allEnums := make([]string, 0)
	allStringEnums := true
	sawFreeScalar := false
	for _, itm := range items {
		t := itm.Get("type").String()
		if t == "null" {
			continue
		}
		if itm.Get("enum").IsArray() {
			for _, ev := range itm.Get("enum").Array() {
				if ev.Type == gjson.Null {
					continue
				}
				allEnums = append(allEnums, ev.String())
			}
		} else {
			allStringEnums = false
			isObjectOrArray := t == "object" || t == "array" || itm.Get("properties").Exists() || itm.Get("items").Exists()
			if !isObjectOrArray {
				sawFreeScalar = true
			}
		}
	}
	if sawFreeScalar {
		selected, _ = sjson.Delete(selected, "enum")
		return selected
	}
	if allStringEnums && len(allEnums) > 0 {
		dedupedEnums := dedupeStrings(allEnums)
		enumBytes, _ := json.Marshal(dedupedEnums)
		selected, _ = sjson.SetRaw(selected, "enum", string(enumBytes))
	}
	return selected
}

// stripFieldFromParentRequired removes parentPath's own field name from
// its grandparent object's "required" array — called when flattenAnyOfOneOf
// just marked parentPath nullable, since a nullable property may
// legitimately be omitted and so can't stay required.
func stripFieldFromParentRequired(jsonStr *string, parentPath string) {
	parts := splitGJSONPath(parentPath)
	if len(parts) < 2 || parts[len(parts)-2] != "properties" {
		return
	}
	fieldName := unescapeGJSONPathKey(parts[len(parts)-1])
	objectPath := strings.Join(parts[:len(parts)-2], ".")
	reqPath := "required"
	if objectPath != "" {
		reqPath = objectPath + ".required"
	}
	req := gjson.Get(*jsonStr, reqPath)
	if !req.IsArray() {
		return
	}
	var filtered []string
	for _, r := range req.Array() {
		if r.String() != fieldName {
			filtered = append(filtered, r.String())
		}
	}
	if len(filtered) == 0 {
		*jsonStr, _ = sjson.Delete(*jsonStr, reqPath)
	} else {
		*jsonStr, _ = sjson.Set(*jsonStr, reqPath, filtered)
	}
}

// spliceSelectedIntoParent merges selected's top-level keys into whatever
// already lives at parentPath — NOT a full replace (setRawAt), which would
// destroy any sibling property declared alongside anyOf/oneOf at the same
// level (e.g. a base "action" field plus variant-specific ones:
// {"type":"object","properties":{"action":...},"oneOf":[...]}), and would
// be total at the schema root (parentPath=="" means "replace the whole
// document"). "properties"/"required"/"description" each get their own
// merge rule (union properties key-by-key, recursing into a colliding
// nested object/array-items schema; union required; always overwrite
// description, since selected's is already the fully-combined one);
// everything else only fills in if parentPath doesn't already have it.
// Deletes the now-redundant anyOf/oneOf array at anyOfPath afterward.
func spliceSelectedIntoParent(jsonStr, parentPath, selected, anyOfPath string) string {
	selParsed := gjson.Parse(selected)
	selParsed.ForEach(func(k, v gjson.Result) bool {
		skey := k.String()
		switch skey {
		case "properties":
			if !v.IsObject() {
				return true
			}
			v.ForEach(func(pk, pv gjson.Result) bool {
				jsonStr = mergeBranchPropertyIntoParent(jsonStr, parentPath, pk.String(), pv)
				return true
			})
			return true
		case "description":
			destPath := joinPath(parentPath, "description")
			jsonStr, _ = sjson.Set(jsonStr, destPath, v.String())
			return true
		case "type":
			// An audit caught that "type" fell through to the generic
			// exists-guarded fallback below — when the parent already
			// declares "type":"object" (the common shape for a field
			// written as {"type":"object","anyOf":[{"type":"string"},
			// ...]}, before this flattening resolves which branch
			// actually applies), that guard sees "type" already present
			// and skips it, so the resolved branch's real scalar type is
			// dropped and the parent stays "object" — then the
			// empty-OBJECT placeholder logic downstream injects a
			// synthetic "reason" field into what should be a plain
			// string/number/boolean. The flattened type is always the
			// authoritative one here; always overwrite, UNLESS the parent
			// actually carries non-empty properties (where forcing scalar
			// produces invalid {"type":"string","properties":...}).
			destPath := joinPath(parentPath, "type")
			valType := v.String()
			if props := gjson.Get(jsonStr, joinPath(parentPath, "properties")); props.IsObject() && len(props.Map()) > 0 && valType != "object" {
				valType = "object"
			}
			jsonStr, _ = sjson.Set(jsonStr, destPath, valType)
			return true
		case "required":
			if !v.IsArray() {
				return true
			}
			destPath := joinPath(parentPath, "required")
			existing := gjson.Get(jsonStr, destPath)
			seen := make(map[string]bool)
			var merged []string
			if existing.IsArray() {
				for _, r := range existing.Array() {
					if !seen[r.String()] {
						seen[r.String()] = true
						merged = append(merged, r.String())
					}
				}
			}
			for _, r := range v.Array() {
				if !seen[r.String()] {
					seen[r.String()] = true
					merged = append(merged, r.String())
				}
			}
			jsonStr, _ = sjson.Set(jsonStr, destPath, merged)
			return true
		}
		destPath := joinPath(parentPath, skey)
		if !gjson.Get(jsonStr, destPath).Exists() {
			jsonStr, _ = sjson.SetRaw(jsonStr, destPath, v.Raw)
		}
		return true
	})
	jsonStr, _ = sjson.Delete(jsonStr, anyOfPath)
	return jsonStr
}

// mergeBranchPropertyIntoParent merges one property (pk/pv) of the
// selected branch into parentPath's own "properties" — a property already
// declared on the parent (a base field sitting alongside oneOf) gets its
// nested "properties" recursively merged and its "enum" unioned, rather
// than being skipped outright and losing every branch-specific
// refinement.
func mergeBranchPropertyIntoParent(jsonStr, parentPath, key string, pv gjson.Result) string {
	propDest := joinPath(parentPath, "properties."+escapeGJSONPathKey(key))
	if !gjson.Get(jsonStr, propDest).Exists() {
		jsonStr, _ = sjson.SetRaw(jsonStr, propDest, pv.Raw)
		return jsonStr
	}
	if branchProps := pv.Get("properties"); branchProps.IsObject() && gjson.Get(jsonStr, joinPath(propDest, "properties")).IsObject() {
		merged := mergeAllOfPropertiesInto(gjson.Get(jsonStr, propDest).Raw, "properties", branchProps, false)
		jsonStr, _ = sjson.SetRaw(jsonStr, propDest, merged)
	} else if branchItemsProps := pv.Get("items.properties"); branchItemsProps.IsObject() && gjson.Get(jsonStr, joinPath(propDest, "items.properties")).IsObject() {
		merged := mergeAllOfPropertiesInto(gjson.Get(jsonStr, propDest).Raw, "items.properties", branchItemsProps, false)
		if branchItemsReq := pv.Get("items.required"); branchItemsReq.IsArray() {
			existingReq := gjson.Get(merged, "items.required")
			if existingReq.IsArray() {
				seen := make(map[string]bool)
				for _, r := range existingReq.Array() {
					seen[r.String()] = true
				}
				var intersected []string
				for _, r := range branchItemsReq.Array() {
					if seen[r.String()] {
						intersected = append(intersected, r.String())
					}
				}
				if len(intersected) > 0 {
					merged, _ = sjson.Set(merged, "items.required", intersected)
				} else {
					merged, _ = sjson.Delete(merged, "items.required")
				}
			}
		}
		jsonStr, _ = sjson.SetRaw(jsonStr, propDest, merged)
	}
	branchEnum := pv.Get("enum")
	if !branchEnum.IsArray() {
		return jsonStr
	}
	existingEnumPath := joinPath(propDest, "enum")
	existing := gjson.Get(jsonStr, existingEnumPath)
	seen := make(map[string]bool)
	var merged []string
	for _, e := range existing.Array() {
		s := e.String()
		if !seen[s] {
			seen[s] = true
			merged = append(merged, s)
		}
	}
	for _, e := range branchEnum.Array() {
		s := e.String()
		if !seen[s] {
			seen[s] = true
			merged = append(merged, s)
		}
	}
	if len(merged) > 0 {
		jsonStr, _ = sjson.Set(jsonStr, existingEnumPath, merged)
	}
	return jsonStr
}
