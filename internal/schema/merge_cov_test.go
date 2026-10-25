package schema

import (
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
)

func TestMergeAllOf_PropertiesRequiredAndKeywordCopy(t *testing.T) {
	in := `{"allOf":[
		{"properties":{"a":{"type":"string"}},"required":["a"],"description":"first"},
		{"properties":{"b":{"type":"integer"}},"required":["a","b"],"format":"date","enum":["x"]}
	]}`
	out := mergeAllOf(in)
	if gjson.Get(out, "allOf").Exists() {
		t.Fatalf("allOf should be removed: %s", out)
	}
	if gjson.Get(out, "type").String() != "object" {
		t.Errorf("type: %s", out)
	}
	if !gjson.Get(out, "properties.a").Exists() || !gjson.Get(out, "properties.b").Exists() {
		t.Errorf("properties not merged: %s", out)
	}
	if got := getStrings(out, "required"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("required = %v", got)
	}
	if gjson.Get(out, "description").String() != "first" || gjson.Get(out, "format").String() != "date" {
		t.Errorf("keywords not copied: %s", out)
	}
}

func TestMergeAllOf_ParentKeywordsWinAndNonArrayIgnored(t *testing.T) {
	out := mergeAllOf(`{"type":"object","description":"mine","allOf":[{"description":"theirs","minimum":3}]}`)
	if gjson.Get(out, "description").String() != "mine" {
		t.Errorf("parent description overwritten: %s", out)
	}
	if gjson.Get(out, "minimum").Int() != 3 {
		t.Errorf("unsupported constraint should be copied: %s", out)
	}
	notArr := `{"allOf":{"a":1}}`
	if got := mergeAllOf(notArr); got != notArr {
		t.Errorf("non-array allOf should be untouched: %s", got)
	}
}

func TestMergeAllOfPropertiesInto_CollisionBranches(t *testing.T) {
	base := `{"properties":{"p":{"description":"one","properties":{"x":{"type":"string"}},"required":["x","y"],"items":{"properties":{"i":{"type":"string"}},"required":["i","j"]}}}}`
	inc := gjson.Parse(`{"p":{"type":"object","description":"two","properties":{"y":{"type":"string"}},"required":["y","z"],"items":{"properties":{"k":{"type":"string"}},"required":["j","k"]},"title":"T"}}`)

	union := mergeAllOfPropertiesInto(base, "properties", inc, true)
	if got := getStrings(union, "properties.p.required"); !reflect.DeepEqual(got, []string{"x", "y", "z"}) {
		t.Errorf("union required = %v", got)
	}
	if got := getStrings(union, "properties.p.items.required"); !reflect.DeepEqual(got, []string{"i", "j", "k"}) {
		t.Errorf("union items.required = %v", got)
	}
	for _, path := range []string{"properties.p.properties.x", "properties.p.properties.y", "properties.p.items.properties.i", "properties.p.items.properties.k"} {
		if !gjson.Get(union, path).Exists() {
			t.Errorf("missing %s: %s", path, union)
		}
	}
	if gjson.Get(union, "properties.p.type").String() != "object" {
		t.Errorf("type: %s", union)
	}
	if gjson.Get(union, "properties.p.description").String() != "one (two)" {
		t.Errorf("description: %q", gjson.Get(union, "properties.p.description").String())
	}
	if gjson.Get(union, "properties.p.title").String() != "T" {
		t.Errorf("new attribute should be copied: %s", union)
	}

	inter := mergeAllOfPropertiesInto(base, "properties", inc, false)
	if got := getStrings(inter, "properties.p.required"); !reflect.DeepEqual(got, []string{"y"}) {
		t.Errorf("intersect required = %v", got)
	}
	if got := getStrings(inter, "properties.p.items.required"); !reflect.DeepEqual(got, []string{"j"}) {
		t.Errorf("intersect items.required = %v", got)
	}
}

func TestMergeAllOfPropertiesInto_EmptyIntersectionDeletesRequired(t *testing.T) {
	base := `{"properties":{"p":{"type":"object","required":["a"],"items":{"required":["a"]}}}}`
	inc := gjson.Parse(`{"p":{"required":["b"],"items":{"required":["b"]}}}`)
	out := mergeAllOfPropertiesInto(base, "properties", inc, false)
	if gjson.Get(out, "properties.p.required").Exists() {
		t.Errorf("empty intersection must delete required: %s", out)
	}
	if gjson.Get(out, "properties.p.items.required").Exists() {
		t.Errorf("empty items intersection must delete required: %s", out)
	}
}

func TestMergeAllOfPropertiesInto_RequiredAdoptedWhenMissingAndItemsAdopted(t *testing.T) {
	base := `{"properties":{"p":{"type":"string"},"q":{"type":"array"}}}`
	inc := gjson.Parse(`{"p":{"required":["a","a"],"properties":{"a":{"type":"string"}}},"q":{"items":{"type":"string"}}}`)
	out := mergeAllOfPropertiesInto(base, "properties", inc, true)
	if got := getStrings(out, "properties.p.required"); !reflect.DeepEqual(got, []string{"a", "a"}) {
		t.Errorf("required adopted verbatim = %v", got)
	}
	if gjson.Get(out, "properties.p.type").String() != "object" {
		t.Errorf("properties present should force type object: %s", out)
	}
	if gjson.Get(out, "properties.q.items.type").String() != "string" {
		t.Errorf("items should be adopted: %s", out)
	}
	if gjson.Get(out, "properties.q.type").String() != "array" {
		t.Errorf("type: %s", out)
	}
}

func TestMergeAllOfPropertiesInto_FillsMissingTypeAndEscapedKeys(t *testing.T) {
	base := `{"properties":{"a.b":{"description":"d"}}}`
	inc := gjson.Parse(`{"a.b":{"type":"number","description":"d"}}`)
	out := mergeAllOfPropertiesInto(base, "properties", inc, true)
	if got := gjson.Get(out, `properties.a\.b.type`).String(); got != "number" {
		t.Errorf("missing type not filled: %s", out)
	}
	if got := gjson.Get(out, `properties.a\.b.description`).String(); got != "d" {
		t.Errorf("identical description must not be duplicated: %q", got)
	}
}

func TestInferEnumBranchType(t *testing.T) {
	cases := map[string]string{
		`[]`:          "null",
		`[null]`:      "null",
		`[null,5]`:    "number",
		`[true]`:      "boolean",
		`[false,1]`:   "boolean",
		`["a",1]`:     "string",
		`[null,"a"]`:  "string",
		`[1.5,"a"]`:   "number",
		`[{"k":1}]`:   "string",
		`["x","y"]`:   "string",
		`[null,null]`: "null",
	}
	for in, want := range cases {
		if got := inferEnumBranchType(gjson.Parse(in)); got != want {
			t.Errorf("inferEnumBranchType(%s) = %q want %q", in, got, want)
		}
	}
}

func TestDescribeBranch(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"type":"object","properties":{"b":{"type":"integer"},"a":{}}}`, "object {a: any, b: integer}"},
		{`{"properties":{}}`, "object {}"},
		{`{"type":"array","items":{"type":"number"}}`, "array of number"},
		{`{"items":{}}`, "array of any"},
		{`{"type":"array"}`, "array of any"},
		{`{"enum":["a","b"]}`, "string, one of [a, b]"},
		{`{"type":"integer","enum":[1,2]}`, "integer, one of [1, 2]"},
		{`{"type":"boolean"}`, "boolean"},
		{`{}`, "string"},
	}
	for _, c := range cases {
		if got := describeBranch(gjson.Parse(c.in)); got != c.want {
			t.Errorf("describeBranch(%s) = %q want %q", c.in, got, c.want)
		}
	}
}

func TestDescribeBranch_TruncatesLongObjects(t *testing.T) {
	in := `{"properties":{"a":{},"b":{},"c":{},"d":{},"e":{},"f":{},"g":{},"h":{},"i":{},"j":{},"k":{},"l":{},"m":{},"n":{}}}`
	got := describeBranch(gjson.Parse(in))
	want := "object {a: any, b: any, c: any, d: any, e: any, f: any, g: any, h: any, i: any, j: any, k: any, l: any, ...}"
	if got != want {
		t.Errorf("got %q", got)
	}
}
