package schema

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func strs(r gjson.Result) string {
	var out []string
	for _, v := range r.Array() {
		out = append(out, v.String())
	}
	return strings.Join(out, ",")
}

func TestFlattenAnyOf_BaseObjectPropertyGetsBranchSubPropertiesMergedIn(t *testing.T) {
	in := `{"type":"object","properties":{"cfg":{"type":"object","properties":{"a":{"type":"string"}}}},
		"oneOf":[{"type":"object","properties":{"cfg":{"type":"object","properties":{"b":{"type":"integer"}}}}}]}`
	out := flattenAnyOfOneOf(in, true)
	if gjson.Get(out, "properties.cfg.properties.a.type").String() != "string" || gjson.Get(out, "properties.cfg.properties.b.type").String() != "integer" {
		t.Fatalf("both the base and the branch sub-properties must survive: %s", out)
	}
	if gjson.Get(out, "oneOf").Exists() {
		t.Fatalf("the oneOf must be gone after flattening: %s", out)
	}
}

func TestFlattenAnyOf_BaseArrayOfObjects_ItemsMergedAndRequiredIntersected(t *testing.T) {
	base := func(req string) string {
		return `{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"a":{"type":"string"}},"required":` + req + `}}},
			"oneOf":[{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"b":{"type":"string"}},"required":["a","y"]}}}}]}`
	}
	out := flattenAnyOfOneOf(base(`["a","x"]`), true)
	if !gjson.Get(out, "properties.rows.items.properties.a").Exists() || !gjson.Get(out, "properties.rows.items.properties.b").Exists() {
		t.Fatalf("item properties from both sides expected: %s", out)
	}
	// Only a field both sides require stays required; one branch's field cannot be demanded of the other.
	if got := strs(gjson.Get(out, "properties.rows.items.required")); got != "a" {
		t.Fatalf("items.required = [%s], want the intersection [a]\n%s", got, out)
	}
	out = flattenAnyOfOneOf(base(`["x"]`), true)
	if gjson.Get(out, "properties.rows.items.required").Exists() {
		t.Fatalf("no common required field: the list must disappear, got %s", out)
	}
}

func TestFlattenAnyOf_EnumsOfBaseAndBranchAreUnionedWithoutDuplicates(t *testing.T) {
	in := `{"type":"object","properties":{"action":{"type":"string","enum":["a","b"]}},
		"oneOf":[{"type":"object","properties":{"action":{"type":"string","enum":["b","c"]}}}]}`
	out := flattenAnyOfOneOf(in, true)
	if got := strs(gjson.Get(out, "properties.action.enum")); got != "a,b,c" {
		t.Fatalf("enum = [%s], want a,b,c in first-seen order\n%s", got, out)
	}
}

func TestFlattenAnyOf_BranchWithoutEnumLeavesTheBaseEnumAlone(t *testing.T) {
	in := `{"type":"object","properties":{"action":{"type":"string","enum":["a","b"]}},
		"oneOf":[{"type":"object","properties":{"action":{"type":"string"}}}]}`
	out := flattenAnyOfOneOf(in, true)
	if got := strs(gjson.Get(out, "properties.action.enum")); got != "a,b" {
		t.Fatalf("enum = [%s], want the base's a,b untouched\n%s", got, out)
	}
}

func TestFlattenAnyOf_EmptyAnyOfIsRemoved(t *testing.T) {
	for _, key := range []string{"anyOf", "oneOf"} {
		out := flattenAnyOfOneOf(`{"type":"string","`+key+`":[]}`, true)
		if gjson.Get(out, key).Exists() || gjson.Get(out, "type").String() != "string" {
			t.Errorf("empty %s must be dropped and the rest kept: %s", key, out)
		}
	}
}

func TestFlattenAnyOf_ParentTypeIsOverriddenByTheResolvedScalarBranch(t *testing.T) {
	out := flattenAnyOfOneOf(`{"type":"object","anyOf":[{"type":"string"}]}`, true)
	if gjson.Get(out, "type").String() != "string" {
		t.Fatalf("a scalar branch is authoritative over a stale parent type: %s", out)
	}
	// ...but never a scalar type on top of real properties.
	out = flattenAnyOfOneOf(`{"type":"object","properties":{"k":{"type":"string"}},"anyOf":[{"type":"string"}]}`, true)
	if gjson.Get(out, "type").String() != "object" {
		t.Fatalf("a parent that carries properties must stay an object: %s", out)
	}
}

func TestFlattenAnyOf_RequiredIsUnionedAndDescriptionOverwritten(t *testing.T) {
	in := `{"type":"object","description":"old","required":["x"],"properties":{"x":{"type":"string"}},
		"oneOf":[{"type":"object","description":"new","properties":{"y":{"type":"string"}},"required":["x","y"]}]}`
	out := flattenAnyOfOneOf(in, true)
	if got := strs(gjson.Get(out, "required")); got != "x,y" {
		t.Fatalf("required = [%s], want x,y without duplicates\n%s", got, out)
	}
	if got := gjson.Get(out, "description").String(); !strings.Contains(got, "new") {
		t.Fatalf("the branch's (already combined) description must win, got %q", got)
	}
}

func TestFlattenAnyOf_ParentKeysAreNotOverwrittenByTheBranch(t *testing.T) {
	in := `{"type":"object","title":"keep me","oneOf":[{"type":"object","title":"branch title","format":"x","properties":{"y":{"type":"string"}}}]}`
	out := flattenAnyOfOneOf(in, true)
	if gjson.Get(out, "title").String() != "keep me" {
		t.Fatalf("a key the parent already has stays: %s", out)
	}
	if gjson.Get(out, "format").String() != "x" {
		t.Fatalf("a key only the branch has is filled in: %s", out)
	}
}
