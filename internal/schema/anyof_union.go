// Part of the schema package — see jsonpath.go for the package doc
// comment. A union whose non-null branches have DIFFERENT value kinds
// (string | array, integer | string, object | string ...) cannot be
// expressed in the Gemini schema subset. Keeping only the best-scoring
// branch (the earlier behavior) silently told the model that the other
// forms do not exist. Instead the field is declared as a string whose
// description enumerates every accepted form, and the response path
// (toolargs.ConformToSchema, driven by the ORIGINAL schema) parses the
// string back into the array/object/number the client declared.
//
// TODO(parametersJsonSchema): if the upstream is ever verified to accept
// parametersJsonSchema, send the original union untouched instead and
// keep this degradation only as the fallback for a 400.
package schema

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// valueKind collapses a branch's type into the family that matters for
// "can one JSON type represent both".
func valueKind(t string) string {
	switch t {
	case "integer", "number":
		return "number"
	}
	return t
}

// mixedUnionSchema returns the string-typed replacement schema for items
// when their non-null branches span more than one value kind.
func mixedUnionSchema(items []gjson.Result, allTypes []string) (string, bool) {
	kinds := map[string]bool{}
	for _, t := range allTypes {
		if t != "null" && t != "" {
			kinds[valueKind(t)] = true
		}
	}
	if len(kinds) < 2 {
		return "", false
	}
	var forms []string
	for _, itm := range items {
		if itm.Get("type").String() == "null" {
			continue
		}
		forms = append(forms, describeBranch(itm))
	}
	desc := "Accepts one of: " + strings.Join(dedupeStrings(forms), " | ") +
		". For any non-string form, pass it as a JSON-encoded string."
	out := `{"type":"string"}`
	out, _ = sjson.Set(out, "description", desc)
	return out, true
}

func describeBranch(b gjson.Result) string {
	t := b.Get("type").String()
	switch {
	case t == "object" || b.Get("properties").Exists():
		var names []string
		b.Get("properties").ForEach(func(k, v gjson.Result) bool {
			vt := v.Get("type").String()
			if vt == "" {
				vt = "any"
			}
			names = append(names, k.String()+": "+vt)
			return true
		})
		sort.Strings(names)
		if len(names) > 12 {
			names = append(names[:12], "...")
		}
		return fmt.Sprintf("object {%s}", strings.Join(names, ", "))
	case t == "array" || b.Get("items").Exists():
		it := b.Get("items.type").String()
		if it == "" {
			it = "any"
		}
		return "array of " + it
	case b.Get("enum").IsArray():
		var vals []string
		for _, e := range b.Get("enum").Array() {
			vals = append(vals, e.String())
		}
		return fmt.Sprintf("%s, one of [%s]", orDefault(t, "string"), strings.Join(vals, ", "))
	}
	return orDefault(t, "string")
}
