// Part of the schema package — see jsonpath.go for the package doc
// comment. Direct port of v1's mergeAllOf/mergeAllOfPropertiesInto.
package schema

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// mergeAllOfPropertiesInto merges one allOf branch's "properties" object
// into whatever already lives at propsDestPath (another branch's own
// "properties", possibly already merged from earlier branches), recursing
// into nested "properties"/"items" on a collision instead of treating
// them as opaque atomic values. unionRequired selects how a colliding
// nested object's own "required" list combines: UNION (true — correct for
// a genuine allOf, where every branch's constraints apply simultaneously,
// so a field required by any branch really is required) or INTERSECTION
// (false — correct when this same merge is reused one level down for an
// anyof.go oneOf/anyOf collision, where branches are mutually exclusive
// alternatives and only a field required by ALL of them is genuinely
// always required).
func mergeAllOfPropertiesInto(jsonStr, propsDestPath string, props gjson.Result, unionRequired bool) string {
	props.ForEach(func(key, value gjson.Result) bool {
		destPath := joinPath(propsDestPath, escapeGJSONPathKey(key.String()))
		if !gjson.Get(jsonStr, destPath).Exists() {
			jsonStr, _ = sjson.SetRaw(jsonStr, destPath, value.Raw)
			return true
		}
		// Deep merge collision: preserve existing type if incoming lacks
		// one, merge description hints.
		existingType := gjson.Get(jsonStr, destPath+".type").String()
		incomingType := value.Get("type").String()
		if existingType == "" && incomingType != "" {
			jsonStr, _ = sjson.Set(jsonStr, destPath+".type", incomingType)
		}
		existingDesc := gjson.Get(jsonStr, destPath+".description").String()
		incomingDesc := value.Get("description").String()
		if incomingDesc != "" && incomingDesc != existingDesc {
			jsonStr = appendHint(jsonStr, destPath, incomingDesc)
		}
		value.ForEach(func(attrKey, attrVal gjson.Result) bool {
			k := attrKey.String()
			if k == "description" || k == "type" {
				return true
			}
			if k == "properties" && attrVal.IsObject() {
				jsonStr = mergeAllOfPropertiesInto(jsonStr, destPath+".properties", attrVal, unionRequired)
				return true
			}
			if k == "items" && attrVal.IsObject() {
				// Two allOf branches can both constrain an array's element
				// schema (one adds a "name" field to items, another adds
				// "id") — merge rather than keep only the first branch's.
				itemsDestPath := destPath + ".items"
				if !gjson.Get(jsonStr, itemsDestPath).Exists() {
					jsonStr, _ = sjson.SetRaw(jsonStr, itemsDestPath, attrVal.Raw)
					return true
				}
				if itemsProps := attrVal.Get("properties"); itemsProps.IsObject() {
					jsonStr = mergeAllOfPropertiesInto(jsonStr, itemsDestPath+".properties", itemsProps, unionRequired)
				}
				if itemsReq := attrVal.Get("required"); itemsReq.IsArray() {
					reqPath := itemsDestPath + ".required"
					existing := gjson.Get(jsonStr, reqPath)
					var merged []string
					if !existing.IsArray() {
						for _, r := range itemsReq.Array() {
							merged = append(merged, r.String())
						}
					} else if unionRequired {
						seen := make(map[string]bool)
						for _, r := range existing.Array() {
							if !seen[r.String()] {
								seen[r.String()] = true
								merged = append(merged, r.String())
							}
						}
						for _, r := range itemsReq.Array() {
							if !seen[r.String()] {
								seen[r.String()] = true
								merged = append(merged, r.String())
							}
						}
					} else {
						existingSet := make(map[string]bool)
						for _, r := range existing.Array() {
							existingSet[r.String()] = true
						}
						for _, r := range itemsReq.Array() {
							if existingSet[r.String()] {
								merged = append(merged, r.String())
							}
						}
					}
					if len(merged) == 0 {
						jsonStr, _ = sjson.Delete(jsonStr, reqPath)
					} else {
						jsonStr, _ = sjson.Set(jsonStr, reqPath, merged)
					}
				}
				return true
			}
			if k == "required" && attrVal.IsArray() {
				// Two allOf branches colliding on the same nested object,
				// each requiring a DIFFERENT field of it, must union
				// (unionRequired) or intersect (!unionRequired) rather than
				// let the second branch's required array get silently
				// dropped because the first already set one.
				reqPath := destPath + ".required"
				existing := gjson.Get(jsonStr, reqPath)
				var merged []string
				if !existing.IsArray() {
					for _, r := range attrVal.Array() {
						merged = append(merged, r.String())
					}
				} else if unionRequired {
					seen := make(map[string]bool)
					for _, r := range existing.Array() {
						if !seen[r.String()] {
							seen[r.String()] = true
							merged = append(merged, r.String())
						}
					}
					for _, r := range attrVal.Array() {
						if !seen[r.String()] {
							seen[r.String()] = true
							merged = append(merged, r.String())
						}
					}
				} else {
					existingSet := make(map[string]bool)
					for _, r := range existing.Array() {
						existingSet[r.String()] = true
					}
					for _, r := range attrVal.Array() {
						if existingSet[r.String()] {
							merged = append(merged, r.String())
						}
					}
				}
				if len(merged) == 0 {
					// An empty intersection must delete the key outright,
					// not leave a literal JSON null — cleanupRequiredFields
					// only processes paths where "required" IS an array.
					jsonStr, _ = sjson.Delete(jsonStr, reqPath)
				} else {
					jsonStr, _ = sjson.Set(jsonStr, reqPath, merged)
				}
				return true
			}
			if !gjson.Get(jsonStr, destPath+"."+k).Exists() {
				jsonStr, _ = sjson.SetRaw(jsonStr, destPath+"."+k, attrVal.Raw)
			}
			return true
		})
		if gjson.Get(jsonStr, destPath+".properties").IsObject() {
			jsonStr, _ = sjson.Set(jsonStr, destPath+".type", "object")
		} else if gjson.Get(jsonStr, destPath+".items").Exists() {
			jsonStr, _ = sjson.Set(jsonStr, destPath+".type", "array")
		}
		return true
	})
	return jsonStr
}

// mergeAllOf flattens an "allOf" array into its parent node: properties
// merge (mergeAllOfPropertiesInto), required lists union, and a handful of
// other keywords (type/enum/format/description/items/nullable/anyOf/oneOf,
// plus every entry in unsupportedConstraints) copy straight across when the
// parent doesn't already declare them — before the allOf array itself is
// deleted, so a branch's anyOf/oneOf gets a chance to reach flattenAnyOfOneOf
// (which runs immediately after this) instead of being discarded with it.
func mergeAllOf(jsonStr string) string {
	paths := findPaths(jsonStr, "allOf")
	sortByDepth(paths)

	for _, p := range paths {
		allOf := gjson.Get(jsonStr, p)
		if !allOf.IsArray() {
			continue
		}
		parentPath := trimSuffix(p, ".allOf")

		for _, item := range allOf.Array() {
			if props := item.Get("properties"); props.IsObject() {
				if !gjson.Get(jsonStr, joinPath(parentPath, "type")).Exists() {
					jsonStr, _ = sjson.Set(jsonStr, joinPath(parentPath, "type"), "object")
				}
				jsonStr = mergeAllOfPropertiesInto(jsonStr, joinPath(parentPath, "properties"), props, true)
			}
			if req := item.Get("required"); req.IsArray() {
				reqPath := joinPath(parentPath, "required")
				current := getStrings(jsonStr, reqPath)
				for _, r := range req.Array() {
					if s := r.String(); !contains(current, s) {
						current = append(current, s)
					}
				}
				jsonStr, _ = sjson.Set(jsonStr, reqPath, current)
			}
			for _, kw := range append([]string{"type", "enum", "format", "description", "items", "nullable", "anyOf", "oneOf"}, unsupportedConstraints...) {
				if v := item.Get(kw); v.Exists() && !gjson.Get(jsonStr, joinPath(parentPath, kw)).Exists() {
					jsonStr, _ = sjson.SetRaw(jsonStr, joinPath(parentPath, kw), v.Raw)
				}
			}
		}
		jsonStr, _ = sjson.Delete(jsonStr, p)
	}
	return jsonStr
}
