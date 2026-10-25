package schema

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripReasonAtFieldSpec(t *testing.T) {
	for _, c := range []struct {
		name, args, spec, want string
	}{
		{"plain nested object", `{"env":{"reason":"r","A":"1"}}`, "env", `{"env":{"A":"1"}}`},
		{"spec resolves to nothing", `{"env":{"A":"1"}}`, "env", `{"env":{"A":"1"}}`},
		{"unrelated field is untouched", `{"other":{"reason":"keep"}}`, "env", `{"other":{"reason":"keep"}}`},
		{"array of objects (trailing .items)", `{"rows":[{"reason":"a","k":1},{"k":2},{"reason":"b"}]}`, "rows.items", `{"rows":[{"k":1},{"k":2},{}]}`},
		{"dict inside each array element", `{"rows":[{"meta":{"reason":"a","x":1}},{"meta":{"x":2}}]}`, "rows.items.meta", `{"rows":[{"meta":{"x":1}},{"meta":{"x":2}}]}`},
		{"2D array of dicts", `{"m":[[{"meta":{"reason":"a","x":1}}],[{"meta":{"reason":"b"}},{"meta":{"x":3}}]]}`, "m.items.items.meta",
			`{"m":[[{"meta":{"x":1}}],[{"meta":{}},{"meta":{"x":3}}]]}`},
		{"field is not an array", `{"rows":"text"}`, "rows.items", `{"rows":"text"}`},
		{"empty array", `{"rows":[]}`, "rows.items.meta", `{"rows":[]}`},
	} {
		if got := stripReasonAtFieldSpec(c.args, "", c.spec); got != c.want {
			t.Errorf("%s:\n  args %s spec %q\n  got  %s\n  want %s", c.name, c.args, c.spec, got, c.want)
		}
	}
}

func TestStripNestedOpenDictReason_UsesTheRecordedFieldsOfThatSessionAndTool(t *testing.T) {
	setOpenDictArgFields("sessP", "Bash", []string{"env", "rows.items"})
	args := `{"cmd":"ls","env":{"reason":"why","A":"1"},"rows":[{"reason":"x"}]}`
	want := `{"cmd":"ls","env":{"A":"1"},"rows":[{}]}`
	if got := StripNestedOpenDictReason("sessP", "Bash", args); got != want {
		t.Fatalf("got %s\nwant %s", got, want)
	}
	if got := StripNestedOpenDictReason("sessQ", "Bash", args); got != args {
		t.Fatalf("another session must not be affected, got %s", got)
	}
	if got := StripNestedOpenDictReason("sessP", "Other", args); got != args {
		t.Fatalf("another tool must not be affected, got %s", got)
	}
	setOpenDictArgFields("sessP", "Bash", nil) // clears the entry
	if got := StripNestedOpenDictReason("sessP", "Bash", args); got != args {
		t.Fatalf("a cleared entry strips nothing, got %s", got)
	}
}

func TestRemovePlaceholderFields(t *testing.T) {
	in := `{"type":"object","properties":{
		"_":{"type":"boolean"},
		"keep":{"type":"string"},
		"sub":{"type":"object","properties":{"reason":{"type":"string","description":"Brief explanation of why you are calling this tool"}},"required":["reason"]},
		"real":{"type":"object","properties":{"reason":{"type":"string","description":"The reason the user gives"},"id":{"type":"string"}},"required":["reason"]},
		"lonely":{"type":"object","properties":{"reason":{"type":"string","description":"The reason the user gives"}},"required":["reason"]}
	},"required":["_","keep"]}`
	out := removePlaceholderFields(in)
	if gjson.Get(out, "properties._").Exists() {
		t.Errorf("the '_' placeholder must go: %s", out)
	}
	if got := strs(gjson.Get(out, "required")); got != "keep" {
		t.Errorf("'_' must leave required, got [%s]", got)
	}
	if gjson.Get(out, "properties.sub.properties.reason").Exists() || gjson.Get(out, "properties.sub.required").Exists() {
		t.Errorf("a lone placeholder 'reason' (and its now-empty required) must go: %s", out)
	}
	if !gjson.Get(out, "properties.real.properties.reason").Exists() || strs(gjson.Get(out, "properties.real.required")) != "reason" {
		t.Errorf("a real 'reason' next to other fields is the tool's own and must stay: %s", out)
	}
	if !gjson.Get(out, "properties.lonely.properties.reason").Exists() {
		t.Errorf("a lone 'reason' with a user-meaningful description is not a placeholder: %s", out)
	}
}

func TestRemovePlaceholderFields_KeepsOtherRequiredEntries(t *testing.T) {
	in := `{"type":"object","properties":{"a":{"type":"string"},"reason":{"type":"string","description":"Placeholder"}},"required":["a","reason"]}`
	// reason is not alone here (two properties), so nothing is removed.
	if out := removePlaceholderFields(in); out != in {
		t.Fatalf("a 'reason' next to real fields must be left alone: %s", out)
	}
	in = `{"type":"object","properties":{"reason":{"type":"string","description":"Placeholder"}},"required":["reason"]}`
	out := removePlaceholderFields(in)
	if gjson.Get(out, "properties.reason").Exists() || gjson.Get(out, "required").Exists() {
		t.Fatalf("the lone 'Placeholder' reason must go: %s", out)
	}
}

func TestConvertPrefixItemsToItems(t *testing.T) {
	for _, c := range []struct {
		name, in string
		check    func(out string) bool
	}{
		{"single element becomes items", `{"type":"array","prefixItems":[{"type":"string"}]}`,
			func(o string) bool { return gjson.Get(o, "items.type").String() == "string" }},
		{"several elements become a oneOf", `{"type":"array","prefixItems":[{"type":"string"},{"type":"integer"}]}`,
			func(o string) bool {
				r := gjson.Get(o, "items.oneOf").Array()
				return len(r) == 2 && r[0].Get("type").String() == "string" && r[1].Get("type").String() == "integer"
			}},
		{"items:false (closed tuple) does not block the conversion", `{"type":"array","prefixItems":[{"type":"number"}],"items":false}`,
			func(o string) bool { return gjson.Get(o, "items.type").String() == "number" }},
		{"a real items schema wins", `{"type":"array","prefixItems":[{"type":"number"}],"items":{"type":"string"}}`,
			func(o string) bool { return gjson.Get(o, "items.type").String() == "string" }},
		{"empty prefixItems is left to be stripped later", `{"type":"array","prefixItems":[]}`,
			func(o string) bool { return !gjson.Get(o, "items").Exists() }},
		{"nested tuple", `{"type":"object","properties":{"p":{"type":"array","prefixItems":[{"type":"boolean"}]}}}`,
			func(o string) bool { return gjson.Get(o, "properties.p.items.type").String() == "boolean" }},
	} {
		if out := convertPrefixItemsToItems(c.in); !c.check(out) {
			t.Errorf("%s: unexpected result %s", c.name, out)
		}
	}
}
