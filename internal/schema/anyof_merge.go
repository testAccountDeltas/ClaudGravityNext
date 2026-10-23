// Part of the schema package — see jsonpath.go for the package doc
// comment, and anyof.go for why this is a separate file from
// flattenAnyOfOneOf. Direct port of v1's mergeObjectBranches/selectBest.
package schema

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// propEntry tracks one merged property across every object branch that
// declares it.
type propEntry struct {
	raw          string
	constValues  []string
	isConstField bool
	constType    string
	// nestedRequiredSets records EACH branch's own nested required list
	// for this property, so the merge below can take their INTERSECTION
	// (only a sub-field common to every branch that declares this property
	// stays required) rather than their union — unioning would require a
	// sub-field from every branch simultaneously (e.g. a shared "config"
	// object where branch "create" needs config.create_token and branch
	// "delete" needs config.delete_token), making every call fail
	// validation regardless of which action was chosen.
	nestedRequiredSets [][]string
	itemsRequiredSets  [][]string
}

// mergeObjectBranches combines multiple object-typed anyOf/oneOf branches
// (a discriminated union, e.g. action:"create" vs action:"delete") into
// one schema instead of the caller picking just one and discarding the
// rest. Properties are unioned (first branch to declare a given key wins
// on a genuine scalar-type clash, recorded as an "Also accepts: X" hint
// rather than silently discarded; a shared nested object merges
// recursively); a field present in more than one branch as a scalar
// `const` or single/multi-value `enum` (the discriminator itself) merges
// into a combined `enum`; top-level `required` is the INTERSECTION across
// branches, since a field only some branches require isn't safely
// mandatory once every branch's parameters are merged into one object the
// model can fill in for any action.
func mergeObjectBranches(branches []gjson.Result) string {
	order := make([]string, 0)
	props := make(map[string]*propEntry)
	var requiredSets [][]string

	for _, branch := range branches {
		var reqList []string
		if req := branch.Get("required"); req.IsArray() {
			for _, r := range req.Array() {
				reqList = append(reqList, r.String())
			}
		}
		requiredSets = append(requiredSets, reqList)

		if p := branch.Get("properties"); p.IsObject() {
			p.ForEach(func(pk, pv gjson.Result) bool {
				mergeOneBranchProperty(props, &order, pk.String(), pv)
				return true
			})
		}
	}

	mergedProps := buildMergedProps(order, props)

	var mergedRequired []string
	if len(requiredSets) > 0 {
		counts := make(map[string]int)
		for _, set := range requiredSets {
			seen := make(map[string]bool)
			for _, f := range set {
				if !seen[f] {
					seen[f] = true
					counts[f]++
				}
			}
		}
		for _, key := range order {
			if counts[key] == len(requiredSets) {
				mergedRequired = append(mergedRequired, key)
			}
		}
	}

	selected := `{"type":"object"}`
	selected, _ = sjson.SetRaw(selected, "properties", mergedProps)
	if len(mergedRequired) > 0 {
		reqBytes, _ := json.Marshal(mergedRequired)
		selected, _ = sjson.SetRaw(selected, "required", string(reqBytes))
	}
	return selected
}

func mergeOneBranchProperty(props map[string]*propEntry, order *[]string, key string, pv gjson.Result) {
	entry, exists := props[key]
	if !exists {
		entry = &propEntry{raw: pv.Raw}
		props[key] = entry
		*order = append(*order, key)
	} else if pv.Get("properties").IsObject() && gjson.Parse(entry.raw).Get("properties").IsObject() {
		// A shared nested object (e.g. "config") where different branches
		// declare different sub-fields for their own action — merge
		// recursively instead of first-branch-wins, which would silently
		// erase every other branch's own sub-properties for it.
		entry.raw = mergeAllOfPropertiesInto(entry.raw, "properties", pv.Get("properties"), false)
	} else if pv.Get("items.properties").IsObject() && gjson.Parse(entry.raw).Get("items.properties").IsObject() {
		// A shared array of objects where different branches declare different items.properties
		entry.raw = mergeAllOfPropertiesInto(entry.raw, "items.properties", pv.Get("items.properties"), false)
	} else if !(pv.Get("properties").IsObject() || gjson.Parse(entry.raw).Get("properties").IsObject() || pv.Get("items.properties").IsObject() || gjson.Parse(entry.raw).Get("items.properties").IsObject()) {
		// A scalar-vs-scalar collision (branch A declares "id":
		// {"type":"string"}, branch B declares "id":{"type":"integer"}) —
		// record the alternate type/description as a hint rather than
		// silently discarding it.
		existingType := gjson.Parse(entry.raw).Get("type").String()
		newType := pv.Get("type").String()
		if newType != "" && newType != existingType {
			entry.raw = appendHintRaw(entry.raw, "Also accepts: "+newType)
		}
		if newDesc := pv.Get("description").String(); newDesc != "" {
			if existingDesc := gjson.Parse(entry.raw).Get("description").String(); existingDesc == "" {
				entry.raw, _ = sjson.Set(entry.raw, "description", newDesc)
			} else if !strings.Contains(existingDesc, newDesc) {
				entry.raw = appendHintRaw(entry.raw, newDesc)
			}
		}
	}

	if pv.Get("properties").IsObject() {
		var nestedReq []string
		if newReq := pv.Get("required"); newReq.IsArray() {
			for _, r := range newReq.Array() {
				nestedReq = append(nestedReq, r.String())
			}
		}
		entry.nestedRequiredSets = append(entry.nestedRequiredSets, nestedReq)
	}
	if pv.Get("items.properties").IsObject() {
		var itemsReq []string
		if newReq := pv.Get("items.required"); newReq.IsArray() {
			for _, r := range newReq.Array() {
				itemsReq = append(itemsReq, r.String())
			}
		}
		entry.itemsRequiredSets = append(entry.itemsRequiredSets, itemsReq)
	}

	if c := pv.Get("const"); c.Exists() {
		entry.isConstField = true
		entry.constValues = append(entry.constValues, c.String())
		if entry.constType == "" {
			if t := pv.Get("type").String(); t == "integer" || t == "number" || t == "boolean" {
				entry.constType = t
			} else if c.Type == gjson.Number {
				if strings.Contains(c.Raw, ".") {
					entry.constType = "number"
				} else {
					entry.constType = "integer"
				}
			} else if c.Type == gjson.True || c.Type == gjson.False {
				entry.constType = "boolean"
			} else if c.Type == gjson.String {
				entry.constType = "string"
			}
		}
	} else if en := pv.Get("enum"); en.IsArray() && len(en.Array()) >= 1 {
		// A discriminator is sometimes expressed as a single-element enum
		// ("enum":["delete"]) rather than "const" — semantically identical.
		// A single branch can also validly cover MULTIPLE discriminator
		// values at once ("enum":["create","insert"]) — accumulate every
		// element, not only the first.
		entry.isConstField = true
		for _, v := range en.Array() {
			entry.constValues = append(entry.constValues, v.String())
		}
		if entry.constType == "" {
			if t := pv.Get("type").String(); t == "integer" || t == "number" || t == "boolean" {
				entry.constType = t
			} else if inferred := firstNonNullEnumType(en); inferred != "" {
				entry.constType = inferred
			} else {
				entry.constType = "string"
			}
		}
	}
}

func buildMergedProps(order []string, props map[string]*propEntry) string {
	mergedProps := "{}"
	for _, key := range order {
		entry := props[key]
		raw := entry.raw
		if entry.isConstField && len(entry.constValues) > 1 {
			vals := dedupeStrings(entry.constValues)
			if len(vals) > 1 {
				var enumBytes []byte
				switch entry.constType {
				case "integer":
					var intVals []int64
					allParsed := true
					for _, v := range vals {
						if n, err := strconv.ParseInt(v, 10, 64); err == nil {
							intVals = append(intVals, n)
						} else {
							allParsed = false
							break
						}
					}
					if allParsed && len(intVals) > 0 {
						enumBytes, _ = json.Marshal(intVals)
					} else {
						enumBytes, _ = json.Marshal(vals)
					}
				case "number":
					var numVals []float64
					allParsed := true
					for _, v := range vals {
						if f, err := strconv.ParseFloat(v, 64); err == nil {
							numVals = append(numVals, f)
						} else {
							allParsed = false
							break
						}
					}
					if allParsed && len(numVals) > 0 {
						enumBytes, _ = json.Marshal(numVals)
					} else {
						enumBytes, _ = json.Marshal(vals)
					}
				case "boolean":
					var boolVals []bool
					allParsed := true
					for _, v := range vals {
						if b, err := strconv.ParseBool(v); err == nil {
							boolVals = append(boolVals, b)
						} else {
							allParsed = false
							break
						}
					}
					if allParsed && len(boolVals) > 0 {
						enumBytes, _ = json.Marshal(boolVals)
					} else {
						enumBytes, _ = json.Marshal(vals)
					}
				default:
					enumBytes, _ = json.Marshal(vals)
				}
				raw, _ = sjson.Delete(raw, "const")
				raw, _ = sjson.SetRaw(raw, "enum", string(enumBytes))
				if entry.constType != "" && entry.constType != "string" {
					raw, _ = sjson.Set(raw, "type", entry.constType)
					raw, _ = sjson.Set(raw, "x-orig-type", entry.constType)
				} else if !gjson.Get(raw, "type").Exists() {
					raw, _ = sjson.Set(raw, "type", "string")
				}
			}
		}
		if len(entry.nestedRequiredSets) > 0 {
			nestedCounts := make(map[string]int)
			for _, set := range entry.nestedRequiredSets {
				seen := make(map[string]bool)
				for _, f := range set {
					if !seen[f] {
						seen[f] = true
						nestedCounts[f]++
					}
				}
			}
			var nestedMerged []string
			for f, n := range nestedCounts {
				if n == len(entry.nestedRequiredSets) {
					nestedMerged = append(nestedMerged, f)
				}
			}
			sort.Strings(nestedMerged)
			if len(nestedMerged) > 0 {
				raw, _ = sjson.Set(raw, "required", nestedMerged)
			} else {
				raw, _ = sjson.Delete(raw, "required")
			}
		}
		if len(entry.itemsRequiredSets) > 0 {
			itemsCounts := make(map[string]int)
			for _, set := range entry.itemsRequiredSets {
				seen := make(map[string]bool)
				for _, f := range set {
					if !seen[f] {
						seen[f] = true
						itemsCounts[f]++
					}
				}
			}
			var itemsMerged []string
			for f, n := range itemsCounts {
				if n == len(entry.itemsRequiredSets) {
					itemsMerged = append(itemsMerged, f)
				}
			}
			sort.Strings(itemsMerged)
			if len(itemsMerged) > 0 {
				raw, _ = sjson.Set(raw, "items.required", itemsMerged)
			} else {
				raw, _ = sjson.Delete(raw, "items.required")
			}
		}
		mergedProps, _ = sjson.SetRaw(mergedProps, escapeGJSONPathKey(key), raw)
	}
	return mergedProps
}

// selectBest scores every anyOf/oneOf branch (object > array > typed
// scalar > enum-implied scalar > null/unknown) and returns the
// highest-scoring branch's index to use as the union's base schema, plus
// every branch's own inferred type string (for the caller's "Accepts: X |
// Y" hint and nullable detection). An enum-only branch (no explicit
// "type") infers its scalar type from the first NON-null enum element,
// the same rule convertConstToEnum uses for "const" — a purely-null enum
// (convertConstToEnum's own output for {"const": null}) is recorded as
// type "null" and deliberately scored like an explicit {"type":"null"}
// branch (0, never able to outrank a genuinely typed sibling), not like a
// normal enum-typed branch. A branch with NO type signal at all (not even
// an enum, e.g. just {"description": "..."}) falls back to "string"
// rather than being treated as implicitly nullable.
func selectBest(items []gjson.Result) (bestIdx int, types []string) {
	bestScore := -1
	for i, item := range items {
		t := item.Get("type").String()
		score := 0

		switch {
		case t == "object" || item.Get("properties").Exists():
			score, t = 3, orDefault(t, "object")
		case t == "array" || item.Get("items").Exists():
			score, t = 2, orDefault(t, "array")
		case t != "" && t != "null":
			score = 1
		case t == "" && item.Get("enum").IsArray() && len(item.Get("enum").Array()) > 0:
			t = inferEnumBranchType(item.Get("enum"))
			if t != "null" {
				score = 1
			}
		default:
			if t == "" {
				t = "string"
			}
		}

		if t != "" {
			types = append(types, t)
		}
		if score > bestScore {
			bestScore, bestIdx = score, i
		}
	}
	return
}

func inferEnumBranchType(enumArr gjson.Result) string {
	t := "null"
	for _, ev := range enumArr.Array() {
		if ev.Type == gjson.Number {
			t = "number"
			break
		}
		if ev.Type == gjson.True || ev.Type == gjson.False {
			t = "boolean"
			break
		}
		if ev.Type != gjson.Null {
			t = "string" // some other non-null scalar — "string" stands
			break
		}
	}
	return t
}
