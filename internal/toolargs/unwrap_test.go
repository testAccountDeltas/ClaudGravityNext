package toolargs

import (
	"encoding/json"
	"testing"
)

func TestUnwrapScalarArg(t *testing.T) {
	objSchema := `{"type":"object"}`
	arrSchema := `{"type":["array","null"]}`
	strSchema := `{"type":"string"}`
	cases := []struct {
		name, in, want string
		schema         []string
	}{
		{"not json", `nope`, `nope`, nil},
		{"no wrapper", `{"a":1}`, `{"a":1}`, nil},
		{"non object root", `[1]`, `[1]`, nil},
		{"object value", `{"__scalar_arg__":{"a":1}}`, `{"a":1}`, nil},
		{"string stays string", `{"__scalar_arg__":"hello"}`, `{"value":"hello"}`, nil},
		{"number", `{"__scalar_arg__":12}`, `{"value":12}`, nil},
		{"bool", `{"__scalar_arg__":true}`, `{"value":true}`, nil},
		{"null", `{"__scalar_arg__":null}`, `{"value":null}`, nil},
		{"big int exact", `{"__scalar_arg__":12345678901234567890}`, `{"value":12345678901234567890}`, nil},
		{"html literal", `{"__scalar_arg__":"<a>&"}`, `{"value":"<a>&"}`, nil},
		// the double-parse problem: JSON-looking strings stay strings
		{"numeric-looking string no schema", `{"__scalar_arg__":"123"}`, `{"value":"123"}`, nil},
		{"bool-looking string string schema", `{"__scalar_arg__":"true"}`, `{"value":"true"}`, []string{strSchema}},
		{"array-looking string no schema", `{"__scalar_arg__":"[1,2]"}`, `{"value":"[1,2]"}`, nil},
		{"object-looking string string schema", `{"__scalar_arg__":"{\"a\":1}"}`, `{"value":"{\"a\":1}"}`, []string{strSchema}},
		// ... unless the schema says object/array
		{"object-looking string object schema", `{"__scalar_arg__":"{\"a\":1}"}`, `{"a":1}`, []string{objSchema}},
		{"array string array schema", `{"__scalar_arg__":"[1,2]"}`, `{"value":[1,2]}`, []string{arrSchema}},
		{"number string object schema", `{"__scalar_arg__":"123"}`, `{"value":"123"}`, []string{objSchema}},
		{"broken json string object schema", `{"__scalar_arg__":"{oops"}`, `{"value":"{oops"}`, []string{objSchema}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := UnwrapScalarArg(tc.in, tc.schema...); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
}

func TestNormalizeKeepsLargeIntegersExact(t *testing.T) {
	in, _ := json.Marshal(`{"id":12345678901234567890,"n":1.50}`) // double encoded
	out := NormalizeToArgsObject("Any", string(in))
	if out != `{"id":12345678901234567890,"n":1.50}` {
		t.Fatalf("got %s", out)
	}
}

func TestNormalizeRejectsTrailingGarbage(t *testing.T) {
	if out := NormalizeToArgsObject("Any", `{"a":1} trailing`); out != "{}" {
		t.Fatalf("got %s", out)
	}
}
