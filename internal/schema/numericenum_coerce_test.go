package schema

import (
	"sync/atomic"
	"testing"

	"github.com/tidwall/gjson"
)

var enumSeq atomic.Int64

// enumSession returns a session id unique to the caller; the recorded-field
// cache is process-wide.
func enumSession(t *testing.T) string {
	t.Helper()
	return t.Name() + "-" + string(rune('a'+enumSeq.Add(1)%26))
}

func recorded(t *testing.T, sess, tool string) map[string]string {
	t.Helper()
	m, _ := numericEnumFields.Get(numericEnumKey(sess, tool))
	return m
}

func TestRecordNumericEnumFields_Shapes(t *testing.T) {
	for _, c := range []struct {
		name, schema string
		want         map[string]string
	}{
		{"integer, number, boolean", `{"properties":{
			"i":{"type":"integer","enum":[1,2]},
			"n":{"type":"number","enum":[0.5,1.5]},
			"b":{"type":"boolean","enum":[true,false]}}}`,
			map[string]string{"i": "integer", "n": "number", "b": "boolean"}},
		{"type inferred from the enum values", `{"properties":{
			"i":{"enum":[1,2]},"n":{"enum":[0.5,2]},"b":{"enum":[true]}}}`,
			map[string]string{"i": "integer", "n": "number", "b": "boolean"}},
		{"nullable enum: first non-null element decides", `{"properties":{"i":{"enum":[null,3,4]}}}`,
			map[string]string{"i": "integer"}},
		{"x-orig-type wins over type", `{"properties":{"i":{"type":"string","x-orig-type":"integer","enum":["1","2"]}}}`,
			map[string]string{"i": "integer"}},
		{"nested object path", `{"properties":{"a":{"type":"object","properties":{"b":{"type":"integer","enum":[1]}}}}}`,
			map[string]string{"a.b": "integer"}},
		{"array of enum scalars", `{"properties":{"xs":{"type":"array","items":{"type":"integer","enum":[1,2]}}}}`,
			map[string]string{"xs": "array:integer"}},
		{"array of arrays", `{"properties":{"g":{"type":"array","items":{"type":"array","items":{"type":"number","enum":[0.5]}}}}}`,
			map[string]string{"g": "array2D:number"}},
		{"array of objects", `{"properties":{"rows":{"type":"array","items":{"type":"object","properties":{
			"p":{"type":"integer","enum":[1,2]},"ok":{"type":"boolean","enum":[true]}}}}}}`,
			map[string]string{"rows[].p": "arrayObject:integer", "rows[].ok": "arrayObject:boolean"}},
		{"object inside an array item", `{"properties":{"rows":{"type":"array","items":{"type":"object","properties":{
			"o":{"type":"object","properties":{"p":{"type":"integer","enum":[1]}}}}}}}}`,
			map[string]string{"rows[].o.p": "arrayObject:integer"}},
		{"string enums are not numeric", `{"properties":{"s":{"type":"string","enum":["a","b"]},"t":{"type":"integer"}}}`, nil},
		{"no properties at all", `{"type":"string","enum":[1,2]}`, nil},
	} {
		sess := enumSession(t)
		RecordNumericEnumFields(sess, "T", c.schema)
		got := recorded(t, sess, "T")
		if len(got) != len(c.want) {
			t.Errorf("%s: recorded %v, want %v", c.name, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: field %q = %q, want %q (all: %v)", c.name, k, got[k], v, got)
			}
		}
	}
}

func TestRecordNumericEnumFields_ReRecordingReplacesAndClears(t *testing.T) {
	sess := enumSession(t)
	RecordNumericEnumFields(sess, "T", `{"properties":{"i":{"type":"integer","enum":[1]}}}`)
	if recorded(t, sess, "T")["i"] != "integer" {
		t.Fatal("first recording missing")
	}
	RecordNumericEnumFields(sess, "T", `{"properties":{"j":{"type":"number","enum":[0.5]}}}`)
	if got := recorded(t, sess, "T"); len(got) != 1 || got["j"] != "number" {
		t.Fatalf("a new schema must replace the old one, got %v", got)
	}
	RecordNumericEnumFields(sess, "T", `{"properties":{"s":{"type":"string"}}}`)
	if got := recorded(t, sess, "T"); got != nil {
		t.Fatalf("a schema without numeric enums must clear the entry, got %v", got)
	}
	// ...and then coercion is a no-op for that tool.
	if out := CoerceNumericEnumArgs(sess, "T", `{"i":"1"}`); gjson.Get(out, "i").Type != gjson.String {
		t.Fatalf("stale coercion rules survived: %s", out)
	}
}

func TestNumericEnumFields_AreScopedToSessionAndTool(t *testing.T) {
	a, b := enumSession(t)+"A", enumSession(t)+"B"
	RecordNumericEnumFields(a, "search", `{"properties":{"n":{"type":"integer","enum":[1,2]}}}`)
	RecordNumericEnumFields(b, "search", `{"properties":{"n":{"type":"string","enum":["x"]}}}`)
	if out := CoerceNumericEnumArgs(a, "search", `{"n":"2"}`); gjson.Get(out, "n").Type != gjson.Number {
		t.Fatalf("session A must coerce: %s", out)
	}
	if out := CoerceNumericEnumArgs(b, "search", `{"n":"2"}`); gjson.Get(out, "n").Type != gjson.String {
		t.Fatalf("session B's same-named tool must not inherit A's rule: %s", out)
	}
	if out := CoerceNumericEnumArgs(a, "other", `{"n":"2"}`); gjson.Get(out, "n").Type != gjson.String {
		t.Fatalf("another tool in the same session is unaffected: %s", out)
	}
}

func TestCoerceNumericEnumArgs_ScalarConversions(t *testing.T) {
	sess := enumSession(t)
	RecordNumericEnumFields(sess, "T", `{"properties":{
		"i":{"type":"integer","enum":[1,2,3]},"n":{"type":"number","enum":[0.5,1.5]},"b":{"type":"boolean","enum":[true,false]}}}`)
	for _, c := range []struct{ in, want string }{
		{`{"i":"2"}`, `{"i":2}`},
		{`{"i":" 2 "}`, `{"i":2}`},
		{`{"i":"2.0"}`, `{"i":2}`},
		{`{"i":"1e3"}`, `{"i":1000}`},
		{`{"i":"-0"}`, `{"i":0}`},
		{`{"i":"1.5"}`, `{"i":"1.5"}`},                                   // not an integer: left for the client to reject
		{`{"i":"abc"}`, `{"i":"abc"}`},                                   // not a number
		{`{"i":""}`, `{"i":""}`},                                         // empty
		{`{"i":"99999999999999999999"}`, `{"i":"99999999999999999999"}`}, // beyond int64
		{`{"i":"1e30"}`, `{"i":"1e30"}`},                                 // float form beyond int64
		{`{"n":"0.5"}`, `{"n":0.5}`},
		{`{"n":".5"}`, `{"n":0.5}`},
		{`{"n":"2"}`, `{"n":2}`},
		{`{"n":"1e400"}`, `{"n":"1e400"}`}, // overflows float64
		{`{"n":"0x10"}`, `{"n":"0x10"}`},
		{`{"b":"true"}`, `{"b":true}`},
		{`{"b":"False"}`, `{"b":false}`},
		{`{"b":"yes"}`, `{"b":"yes"}`},
		{`{"i":2,"n":0.5,"b":true}`, `{"i":2,"n":0.5,"b":true}`}, // already typed
		{`{"other":"2"}`, `{"other":"2"}`},                       // not a recorded field
		{`{"i":"3","n":"1.5","b":"true","other":"x"}`, `{"i":3,"n":1.5,"b":true,"other":"x"}`},
	} {
		if got := CoerceNumericEnumArgs(sess, "T", c.in); got != c.want {
			t.Errorf("%s\n  got  %s\n  want %s", c.in, got, c.want)
		}
	}
}

// NaN and ±Inf parse as floats but have no JSON spelling: writing them out
// used to corrupt the whole arguments object.
func TestCoerceNumericEnumArgs_NeverEmitsInvalidJSON(t *testing.T) {
	sess := enumSession(t)
	RecordNumericEnumFields(sess, "T", `{"properties":{"n":{"type":"number","enum":[0.5]},"i":{"type":"integer","enum":[1]}}}`)
	for _, v := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "Infinity", "-Infinity"} {
		for _, f := range []string{"n", "i"} {
			in := `{"` + f + `":"` + v + `"}`
			out := CoerceNumericEnumArgs(sess, "T", in)
			if !gjson.Valid(out) {
				t.Errorf("%s produced invalid JSON: %s", in, out)
			} else if gjson.Get(out, f).Type != gjson.String {
				t.Errorf("%s must stay a string, got %s", in, out)
			}
		}
	}
}

func TestCoerceNumericEnumArgs_Collections(t *testing.T) {
	sess := enumSession(t)
	RecordNumericEnumFields(sess, "T", `{"properties":{
		"xs":{"type":"array","items":{"type":"integer","enum":[1,2,3]}},
		"grid":{"type":"array","items":{"type":"array","items":{"type":"number","enum":[0.5,1.5]}}},
		"rows":{"type":"array","items":{"type":"object","properties":{"p":{"type":"integer","enum":[1,2]},"ok":{"type":"boolean","enum":[true]}}}},
		"deep":{"type":"object","properties":{"lvl":{"type":"integer","enum":[1,2]}}}}}`)
	for _, c := range []struct{ name, in, want string }{
		{"array elements, mixed types", `{"xs":["1",2,"x","3"]}`, `{"xs":[1,2,"x",3]}`},
		{"empty array", `{"xs":[]}`, `{"xs":[]}`},
		{"field is not an array", `{"xs":"1"}`, `{"xs":"1"}`},
		{"2D array", `{"grid":[["0.5","1.5"],["1.5",0.5]]}`, `{"grid":[[0.5,1.5],[1.5,0.5]]}`},
		{"2D with a non-array row", `{"grid":["0.5",["0.5"]]}`, `{"grid":["0.5",[0.5]]}`},
		{"array of objects", `{"rows":[{"p":"1","ok":"true"},{"p":2},{"p":"2","ok":"nope"}]}`, `{"rows":[{"p":1,"ok":true},{"p":2},{"p":2,"ok":"nope"}]}`},
		{"array of objects, row without the field", `{"rows":[{"q":"1"}]}`, `{"rows":[{"q":"1"}]}`},
		{"nested object", `{"deep":{"lvl":"2"}}`, `{"deep":{"lvl":2}}`},
		{"missing parents are not invented", `{}`, `{}`},
	} {
		if got := CoerceNumericEnumArgs(sess, "T", c.in); got != c.want {
			t.Errorf("%s:\n  in   %s\n  got  %s\n  want %s", c.name, c.in, got, c.want)
		}
	}
}

func TestCoerceNumericEnumArgs_ScalarArgumentWrapper(t *testing.T) {
	sess := enumSession(t)
	// A non-object root is wrapped by the schema layer as __scalar_arg__.
	RecordNumericEnumFields(sess, "T", `{"properties":{"__scalar_arg__":{"type":"integer","enum":[1,2]}}}`)
	if got := CoerceNumericEnumArgs(sess, "T", `"2"`); got != `2` {
		t.Fatalf("a bare string argument must be converted, got %s", got)
	}
}

func TestCoerceNumericEnumArgs_DegenerateInputs(t *testing.T) {
	sess := enumSession(t)
	RecordNumericEnumFields(sess, "T", `{"properties":{"i":{"type":"integer","enum":[1]}}}`)
	for _, in := range []string{``, `not json`, `[]`, `null`, `{"i":`} {
		_ = CoerceNumericEnumArgs(sess, "T", in) // must not panic
	}
	if got := CoerceNumericEnumArgs("never-recorded", "T", `{"i":"1"}`); got != `{"i":"1"}` {
		t.Fatalf("an unknown tool passes through, got %s", got)
	}
}
