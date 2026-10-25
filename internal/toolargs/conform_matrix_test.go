package toolargs

import (
	"strings"
	"testing"
)

func objSchema(props string) string {
	return `{"type":"object","properties":{` + props + `}}`
}

// TestConformToSchema_Matrix pins how a tool call's arguments are coerced to
// the declared schema: Gemini hands back strings for numbers and booleans,
// stringified arrays/objects and nulls for unset optionals, and a validated
// client rejects each of those.
func TestConformToSchema_Matrix(t *testing.T) {
	intS, numS := objSchema(`"n":{"type":"integer"}`), objSchema(`"n":{"type":"number"}`)
	boolS, strS := objSchema(`"b":{"type":"boolean"}`), objSchema(`"s":{"type":"string"}`)
	intArr := objSchema(`"a":{"type":"array","items":{"type":"integer"}}`)
	for _, c := range []struct{ name, args, schema, want string }{
		// integers
		{"integer from string", `{"n":"3"}`, intS, `{"n":3}`},
		{"integer from 3.0", `{"n":"3.0"}`, intS, `{"n":3}`},
		{"integer from exponent form", `{"n":"1e3"}`, intS, `{"n":1000}`},
		{"integer: explicit plus sign", `{"n":"+7"}`, intS, `{"n":7}`},
		{"integer: surrounding spaces", `{"n":" 7 "}`, intS, `{"n":7}`},
		{"integer: not whole", `{"n":"1.5"}`, intS, `{"n":"1.5"}`},
		{"integer: not a number", `{"n":"abc"}`, intS, `{"n":"abc"}`},
		{"integer: beyond int64", `{"n":"99999999999999999999"}`, intS, `{"n":"99999999999999999999"}`},
		{"integer: float form at the 1e15 safety bound", `{"n":"1e15"}`, intS, `{"n":"1e15"}`},
		// numbers
		{"number from string", `{"n":"2.50"}`, numS, `{"n":2.5}`},
		{"number: leading dot", `{"n":".5"}`, numS, `{"n":0.5}`},
		{"number: NaN has no JSON form", `{"n":"NaN"}`, numS, `{"n":"NaN"}`},
		{"number: overflow", `{"n":"1e400"}`, numS, `{"n":"1e400"}`},
		{"number: hex is not a number", `{"n":"0x10"}`, numS, `{"n":"0x10"}`},
		// booleans
		{"boolean TRUE", `{"b":"TRUE"}`, boolS, `{"b":true}`},
		{"boolean from 1", `{"b":"1"}`, boolS, `{"b":true}`},
		{"boolean from 0", `{"b":"0"}`, boolS, `{"b":false}`},
		{"boolean: yes is not a boolean", `{"b":"yes"}`, boolS, `{"b":"yes"}`},
		// strings
		{"string from number", `{"s":5}`, strS, `{"s":"5"}`},
		{"string from boolean", `{"s":true}`, strS, `{"s":"true"}`},
		// arrays
		{"stringified array", `{"a":"[1,2]"}`, intArr, `{"a":[1,2]}`},
		{"array elements are conformed", `{"a":["1","2"]}`, intArr, `{"a":[1,2]}`},
		{"truncated stringified array stays", `{"a":"[1,"}`, intArr, `{"a":"[1,"}`},
		{"a plain string is not an array", `{"a":"x"}`, objSchema(`"a":{"type":"array","items":{"type":"string"}}`), `{"a":"x"}`},
		{"tuple items apply by position, extras untouched", `{"a":["1","2","3"]}`,
			objSchema(`"a":{"type":"array","items":[{"type":"integer"},{"type":"string"}]}`), `{"a":[1,"2","3"]}`},
		{"implicit array (items without type)", `{"a":["1"]}`, objSchema(`"a":{"items":{"type":"integer"}}`), `{"a":[1]}`},
		// objects
		{"stringified object", `{"o":"{\"k\":\"1\"}"}`, objSchema(`"o":{"type":"object","properties":{"k":{"type":"integer"}}}`), `{"o":{"k":1}}`},
		{"implicit object (properties without type)", `{"o":{"k":"1"}}`, objSchema(`"o":{"properties":{"k":{"type":"integer"}}}`), `{"o":{"k":1}}`},
		{"unknown properties are left alone", `{"x":"1"}`, intS, `{"x":"1"}`},
		// nulls
		{"null for an optional field is dropped", `{"n":null,"m":1}`, objSchema(`"n":{"type":"integer"},"m":{"type":"integer"}`), `{"m":1}`},
		{"null kept: type array with null", `{"n":null}`, objSchema(`"n":{"type":["integer","null"]}`), `{"n":null}`},
		{"null kept: nullable flag", `{"n":null}`, objSchema(`"n":{"type":"integer","nullable":true}`), `{"n":null}`},
		{"null kept: null in the enum", `{"n":null}`, objSchema(`"n":{"enum":[null,1]}`), `{"n":null}`},
		{"null kept: anyOf with a null branch", `{"n":null}`, objSchema(`"n":{"anyOf":[{"type":"integer"},{"type":"null"}]}`), `{"n":null}`},
		{"null kept: oneOf with a null branch", `{"n":null}`, objSchema(`"n":{"oneOf":[{"type":"integer"},{"type":"null"}]}`), `{"n":null}`},
		{"null kept: const null", `{"n":null}`, objSchema(`"n":{"const":null}`), `{"n":null}`},
		{"null array elements are kept (positions matter)", `{"a":[null,"1"]}`, intArr, `{"a":[null,1]}`},
		// unions and composition
		{"type list: a string that already fits is kept", `{"n":"3"}`, objSchema(`"n":{"type":["integer","string"]}`), `{"n":"3"}`},
		{"allOf branches all apply", `{"o":{"a":"1","b":"2"}}`,
			objSchema(`"o":{"allOf":[{"type":"object","properties":{"a":{"type":"integer"}}},{"properties":{"b":{"type":"integer"}}}]}`), `{"o":{"a":1,"b":2}}`},
		// references
		{"$ref", `{"n":"4"}`, `{"type":"object","properties":{"n":{"$ref":"#/$defs/N"}},"$defs":{"N":{"type":"integer"}}}`, `{"n":4}`},
		{"$ref with escaped slash", `{"n":"4"}`, `{"type":"object","properties":{"n":{"$ref":"#/$defs/a~1b"}},"$defs":{"a/b":{"type":"integer"}}}`, `{"n":4}`},
		{"dangling $ref changes nothing", `{"n":"4"}`, `{"type":"object","properties":{"n":{"$ref":"#/$defs/Missing"}},"$defs":{}}`, `{"n":"4"}`},
		{"self-referencing $ref terminates", `{"n":"4"}`, `{"type":"object","properties":{"n":{"$ref":"#/$defs/A"}},"$defs":{"A":{"$ref":"#/$defs/A"}}}`, `{"n":"4"}`},
		// enums
		{"enum matched case-insensitively", `{"e":"HIGH"}`, objSchema(`"e":{"type":"string","enum":["high","low"]}`), `{"e":"high"}`},
		// things that are not touched at all
		{"scalar root schema", `{"x":1}`, `{"type":"string"}`, `{"x":1}`},
		{"arguments that are not JSON", `not json`, intS, `not json`},
		{"arguments that are not an object", `[1]`, intS, `[1]`},
		{"schema that is not JSON", `{"n":"1"}`, `nope`, `{"n":"1"}`},
	} {
		got, _ := ConformToSchema(c.args, c.schema)
		if !jsonEqualStr(got, c.want) {
			t.Errorf("%s:\n  args   %s\n  got    %s\n  want   %s", c.name, c.args, got, c.want)
		}
	}
}

func jsonEqualStr(a, b string) bool {
	if a == b {
		return true
	}
	return jsonEqual([]byte(a), []byte(b)) && strings.HasPrefix(strings.TrimSpace(a), "{")
}

func TestConformToSchema_ReportsWhatItCannotRepair(t *testing.T) {
	_, issues := ConformToSchema(`{"e":"zzz"}`, objSchema(`"e":{"type":"string","enum":["high","low"]}`))
	if len(issues) != 1 || !strings.Contains(issues[0], "not in enum") {
		t.Fatalf("issues = %v, want one 'not in enum'", issues)
	}
	_, issues = ConformToSchema(`{}`, `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`)
	if len(issues) != 1 || !strings.Contains(issues[0], "id") {
		t.Fatalf("a missing required argument must be reported (never invented): %v", issues)
	}
}

func TestSchemaTypes(t *testing.T) {
	for _, c := range []struct {
		name string
		s    map[string]interface{}
		want string
	}{
		{"single type", map[string]interface{}{"type": "integer"}, "integer"},
		{"null alone is nothing", map[string]interface{}{"type": "null"}, ""},
		{"list drops null", map[string]interface{}{"type": []interface{}{"string", "null", "integer"}}, "string,integer"},
		{"properties imply object", map[string]interface{}{"properties": map[string]interface{}{}}, "object"},
		{"items imply array", map[string]interface{}{"items": map[string]interface{}{}}, "array"},
		{"explicit type beats implication", map[string]interface{}{"type": "string", "properties": map[string]interface{}{}}, "string"},
		{"nothing declared", map[string]interface{}{}, ""},
	} {
		if got := strings.Join(schemaTypes(c.s), ","); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
