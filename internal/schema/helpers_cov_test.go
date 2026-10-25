package schema

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGetStrings(t *testing.T) {
	doc := `{"r":["a",1,true],"o":{"k":1},"s":"x"}`
	if got := getStrings(doc, "r"); !reflect.DeepEqual(got, []string{"a", "1", "true"}) {
		t.Errorf("got %v", got)
	}
	for _, p := range []string{"o", "s", "missing"} {
		if got := getStrings(doc, p); got != nil {
			t.Errorf("getStrings(%s) = %v want nil", p, got)
		}
	}
}

func TestOrDefault(t *testing.T) {
	if orDefault("", "d") != "d" || orDefault("v", "d") != "v" {
		t.Error("orDefault wrong")
	}
}

func TestMergeDescriptionRaw(t *testing.T) {
	if got := gjson.Get(mergeDescriptionRaw(`{"type":"string"}`, "parent"), "description").String(); got != "parent" {
		t.Errorf("empty child: %q", got)
	}
	same := `{"description":"p"}`
	if got := mergeDescriptionRaw(same, "p"); got != same {
		t.Errorf("identical should be untouched: %s", got)
	}
	if got := gjson.Get(mergeDescriptionRaw(`{"description":"child"}`, "parent"), "description").String(); got != "parent (child)" {
		t.Errorf("combined: %q", got)
	}
}

func TestEscapeUnescapeGJSONPathKey_RoundTrip(t *testing.T) {
	for _, k := range []string{"plain", "a.b", "a*b", "a?b", "x.y*z?w", `back\slash`, ""} {
		esc := escapeGJSONPathKey(k)
		if k != "" && !strings.ContainsAny(k, `.*?\`) && esc != k {
			t.Errorf("plain key altered: %q", esc)
		}
		if strings.ContainsAny(k, ".*?") && esc == k {
			t.Errorf("special key %q not escaped", k)
		}
	}
	if got := unescapeGJSONPathKey(escapeGJSONPathKey("a.b*c?d")); got != "a.b*c?d" {
		t.Errorf("round trip: %q", got)
	}
	cases := map[string]string{
		"noescape":  "noescape",
		`a\.b`:      "a.b",
		`a\\b`:      `a\b`,
		`trailing\`: `trailing\`,
		`\*\?`:      "*?",
	}
	for in, want := range cases {
		if got := unescapeGJSONPathKey(in); got != want {
			t.Errorf("unescape(%q) = %q want %q", in, got, want)
		}
	}
}

func TestSplitGJSONPath_RespectsEscapes(t *testing.T) {
	if got := splitGJSONPath(""); got != nil {
		t.Errorf("empty: %v", got)
	}
	got := splitGJSONPath(`a.b\.c.d`)
	if !reflect.DeepEqual(got, []string{"a", `b\.c`, "d"}) {
		t.Errorf("got %q", got)
	}
}

func TestRemoveUnsupportedKeywords_Behaviours(t *testing.T) {
	in := `{
		"$schema":"x","title":"Root Title","type":"object","additionalProperties":false,
		"properties":{
			"title":{"type":"string","description":"a real property named title"},
			"when":{"type":"string","format":"date-time","title":"When it happens"},
			"uuid":{"type":"string","format":"uuid"},
			"nofmt":{"type":"string","format":""},
			"n":{"type":"integer","minimum":1,"x-custom":true,"description":"keep"},
			"c":{"const":"v","type":"string"}
		}}`
	out := removeUnsupportedKeywords(in)
	if gjson.Get(out, "$schema").Exists() || gjson.Get(out, "additionalProperties").Exists() {
		t.Errorf("schema keywords survive: %s", out)
	}
	if gjson.Get(out, "description").String() != "Root Title" || gjson.Get(out, "title").Exists() {
		t.Errorf("title not promoted/removed: %s", out)
	}
	if !gjson.Get(out, "properties.title").Exists() {
		t.Errorf("property named title must survive: %s", out)
	}
	if gjson.Get(out, "properties.when.format").String() != "date-time" {
		t.Errorf("safe format stripped: %s", out)
	}
	if gjson.Get(out, "properties.when.description").String() != "When it happens" {
		t.Errorf("nested title not promoted: %s", out)
	}
	if gjson.Get(out, "properties.uuid.format").Exists() ||
		gjson.Get(out, "properties.uuid.description").String() != "format: uuid" {
		t.Errorf("unsafe format not hinted+removed: %s", out)
	}
	if gjson.Get(out, "properties.nofmt.format").Exists() || gjson.Get(out, "properties.nofmt.description").Exists() {
		t.Errorf("empty format: %s", out)
	}
	if gjson.Get(out, "properties.n.minimum").Exists() || gjson.Get(out, "properties.n.x-custom").Exists() {
		t.Errorf("constraint/extension survive: %s", out)
	}
	if gjson.Get(out, "properties.c.const").Exists() {
		t.Errorf("const survives: %s", out)
	}
}

func TestRemoveUnsupportedKeywords_KeepsExistingDescriptionOverTitle(t *testing.T) {
	out := removeUnsupportedKeywords(`{"type":"string","title":"T","description":"D"}`)
	if gjson.Get(out, "description").String() != "D" || gjson.Get(out, "title").Exists() {
		t.Errorf("got %s", out)
	}
	out = removeUnsupportedKeywords(`{"type":"string","title":""}`)
	if gjson.Get(out, "description").Exists() {
		t.Errorf("empty title must not create a description: %s", out)
	}
}

func TestCleanupRequiredFields_Edges(t *testing.T) {
	in := `{"type":"object","required":"a","properties":{"a":{"type":"string"}},
	"x":{"required":["a"]},
	"y":{"required":["gone"],"properties":{"a":{}}},
	"z":{"required":["a","n","gone"],"properties":{"a":{},"n":{"nullable":true}}}}`
	out := cleanupRequiredFields(in, true)
	if gjson.Get(out, "required").Exists() {
		t.Errorf("non-array required kept: %s", out)
	}
	if gjson.Get(out, "x.required").Exists() {
		t.Errorf("required without properties kept: %s", out)
	}
	if gjson.Get(out, "y.required").Exists() {
		t.Errorf("all-invalid required kept: %s", out)
	}
	if got := getStrings(out, "z.required"); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("z.required = %v", got)
	}
	out = cleanupRequiredFields(in, false)
	if got := getStrings(out, "z.required"); !reflect.DeepEqual(got, []string{"a", "n"}) {
		t.Errorf("without stripNullable z.required = %v", got)
	}
}

func TestAddMissingPropertyTypes_InfersFromShape(t *testing.T) {
	in := `{"type":"object","properties":{
		"obj":{"properties":{"q":{}}},
		"arr":{"items":{"properties":{"w":{}}}},
		"bare":{},
		"typed":{"type":"integer"},
		"en":{"enum":[1]},
		"any":{"anyOf":[{"type":"string"}]},
		"notobj":"scalar",
		"rows":{"type":"array","items":{"items":{"type":"string"}}}
	}}`
	out := addMissingPropertyTypes(in)
	want := map[string]string{
		"properties.obj.type":                    "object",
		"properties.arr.type":                    "array",
		"properties.arr.items.type":              "object",
		"properties.bare.type":                   "string",
		"properties.typed.type":                  "integer",
		"properties.obj.properties.q.type":       "string",
		"properties.arr.items.properties.w.type": "string",
		"properties.rows.items.type":             "array",
	}
	for p, w := range want {
		if got := gjson.Get(out, p).String(); got != w {
			t.Errorf("%s = %q want %q\n%s", p, got, w, out)
		}
	}
	if gjson.Get(out, "properties.en.type").Exists() || gjson.Get(out, "properties.any.type").Exists() {
		t.Errorf("enum/anyOf properties must not be typed: %s", out)
	}
	// items that already carry a type/enum are skipped.
	out = addMissingPropertyTypes(`{"items":{"enum":["a"]}}`)
	if gjson.Get(out, "items.type").Exists() {
		t.Errorf("enum items typed: %s", out)
	}
	out = addMissingPropertyTypes(`{"items":{}}`)
	if gjson.Get(out, "items.type").String() != "string" {
		t.Errorf("bare items default: %s", out)
	}
}

func TestAddMissingArrayItems_Variants(t *testing.T) {
	out := addMissingArrayItems(`{"type":"object","properties":{"a":{"type":"array"}}}`, true)
	if gjson.Get(out, "properties.a.items").Raw != "{}" {
		t.Errorf("tool default: %s", out)
	}
	out = addMissingArrayItems(`{"type":"object","properties":{"a":{"type":"ARRAY"}}}`, false)
	if gjson.Get(out, "properties.a.items.type").String() != "string" {
		t.Errorf("non-tool default: %s", out)
	}
	out = addMissingArrayItems(`{"type":"array","items":[{"type":"integer"},{"type":"string"}]}`, true)
	if gjson.Get(out, "items.type").String() != "integer" || !strings.Contains(gjson.Get(out, "description").String(), "Tuple items:") {
		t.Errorf("tuple: %s", out)
	}
	out = addMissingArrayItems(`{"type":"array","items":[]}`, true)
	if gjson.Get(out, "items").Raw != "{}" || !strings.Contains(gjson.Get(out, "description").String(), "Tuple items: []") {
		t.Errorf("empty tuple: %s", out)
	}
	keep := `{"type":"array","items":{"type":"number"}}`
	if got := addMissingArrayItems(keep, true); got != keep {
		t.Errorf("existing items modified: %s", got)
	}
	for _, in := range []string{`{"type":["array","null"]}`, `{"contents":[{"type":"array"}]}`} {
		if got := addMissingArrayItems(in, true); got != in {
			t.Errorf("should be untouched: %s", got)
		}
	}
}
