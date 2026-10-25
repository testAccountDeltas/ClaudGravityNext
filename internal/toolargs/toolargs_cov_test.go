package toolargs

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCovRepairUnescapedControlChars(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"clean", `{"a":"b"}`, `{"a":"b"}`},
		{"raw newline cr tab", "{\"a\":\"x\ny\rz\tw\"}", `{"a":"x\ny\rz\tw"}`},
		{"other control char", "{\"a\":\"x\x01y\"}", `{"a":"x\u0001y"}`},
		{"structural whitespace kept", "{\n \"a\": 1\n}", "{\n \"a\": 1\n}"},
		{"single quotes", `{'a': 'b'}`, `{"a": "b"}`},
		{"trailing comma obj", `{"a":1, }`, `{"a":1 }`},
		{"trailing comma arr", "[1,2,\n]", "[1,2\n]"},
		{"comma inside string kept", `{"a":"x, }"}`, `{"a":"x, }"}`},
		{"windows path", `{"p":"C:\Users\x"}`, `{"p":"C:\\Users\\x"}`},
		{"valid unicode escape", `{"p":"\u00e9"}`, `{"p":"\u00e9"}`},
		{"short unicode escape", `{"p":"\uZZ"}`, `{"p":"\\uZZ"}`},
		{"valid escapes kept", `{"p":"a\"b\\c\/d\n"}`, `{"p":"a\"b\\c\/d\n"}`},
		{"escaped then raw newline", "{\"p\":\"a\\\nb\"}", `{"p":"a\\nb"}`},
		{"escaped then raw cr", "{\"p\":\"a\\\rb\"}", `{"p":"a\\rb"}`},
	}
	for _, c := range cases {
		if got := repairUnescapedControlChars(c.in); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestCovRepairResultIsValidJSON(t *testing.T) {
	in := "{\"cmd\":\"echo a\nb\",\"p\":\"C:" + `\Path` + "\",}"
	out := repairUnescapedControlChars(in)
	if !json.Valid([]byte(out)) {
		t.Fatalf("not valid: %q", out)
	}
	if gjson.Get(out, "cmd").String() != "echo a\nb" {
		t.Fatalf("cmd: %q", out)
	}
}

func TestCovPruneTopLevelNulls(t *testing.T) {
	if got := PruneTopLevelNulls(`{"a":null,"b":1,"n":{"x":null}}`); got != `{"b":1,"n":{"x":null}}` {
		t.Fatalf("got %s", got)
	}
	for _, in := range []string{`{"a":1}`, `not json`, `null`, `[1]`, ``} {
		if got := PruneTopLevelNulls(in); got != in {
			t.Errorf("%q must be returned unchanged, got %q", in, got)
		}
	}
	if got := PruneTopLevelNulls(`{"big":12345678901234567890,"z":null}`); got != `{"big":12345678901234567890}` {
		t.Fatalf("number precision lost: %s", got)
	}
}

func TestCovEnumIndexAndMatches(t *testing.T) {
	members := []interface{}{"a", json.Number("1"), true}
	cases := []struct {
		v    interface{}
		want int
	}{
		{"a", 0},
		{json.Number("1"), 1},
		{"1", -1}, // string "1" must not match numeric member
		{true, 2},
		{"true", -1},
		{nil, -1},
		{[]interface{}{}, -1},
		{"zzz", -1},
	}
	for _, c := range cases {
		if got := enumIndex(members, c.v); got != c.want {
			t.Errorf("enumIndex(%#v)=%d want %d", c.v, got, c.want)
		}
	}
	if s, ok := scalarText(map[string]interface{}{}); ok || s != "" {
		t.Fatal("non-scalar must report false")
	}
	c := &conformer{}
	sch := func(s string) map[string]interface{} {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(s), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := []struct {
		v      interface{}
		schema string
		want   bool
	}{
		{"x", `{"type":"string"}`, true},
		{json.Number("1"), `{"type":"string"}`, false},
		{true, `{"type":"boolean"}`, true},
		{"x", `{"type":"boolean"}`, false},
		{json.Number("3"), `{"type":"integer"}`, true},
		{json.Number("3.5"), `{"type":"integer"}`, false},
		{"3", `{"type":"integer"}`, false},
		{json.Number("3.5"), `{"type":"number"}`, true},
		{[]interface{}{}, `{"type":"array"}`, true},
		{"x", `{"type":"array"}`, false},
		{map[string]interface{}{}, `{"type":"object"}`, true},
		{"x", `{"type":"object"}`, false},
		{"x", `{}`, true},
		{"x", `{"type":"weird"}`, true},
		{"x", `{"type":["integer","string"]}`, true},
		{"b", `{"type":"string","enum":["a"]}`, false},
		{"a", `{"type":"string","enum":["a"]}`, true},
		{"k", `{"const":"k"}`, true},
		{"j", `{"const":"k"}`, false},
	}
	for _, tc := range m {
		if got := c.matches(tc.v, sch(tc.schema)); got != tc.want {
			t.Errorf("matches(%#v, %s)=%v want %v", tc.v, tc.schema, got, tc.want)
		}
	}
}

func TestCovUnionBranches(t *testing.T) {
	sch := `{"type":"object","properties":{"u":{"anyOf":[{"type":"null"},{"type":"integer"},{"type":"array","items":{"type":"string"}}]},"e":{"anyOf":[{"type":"string","enum":["in_progress","done"]},{"type":"integer"}]}}}`
	// exact branch match, null branch skipped
	out, _ := ConformToSchema(`{"u":5}`, sch)
	if gjson.Get(out, "u").Raw != "5" {
		t.Fatalf("got %s", out)
	}
	// JSON-looking string that is invalid JSON is not decoded into array
	out, _ = ConformToSchema(`{"u":"[not json"}`, sch)
	if gjson.Get(out, "u").Type != gjson.String {
		t.Fatalf("got %s", out)
	}
	// string coercible to the integer branch
	out, _ = ConformToSchema(`{"u":"7"}`, sch)
	if gjson.Get(out, "u").Raw != "7" {
		t.Fatalf("got %s", out)
	}
	// near-miss enum fixed within the string branch
	out, _ = ConformToSchema(`{"e":"in-progress"}`, sch)
	if gjson.Get(out, "e").String() != "in_progress" {
		t.Fatalf("got %s", out)
	}
	// JSON string for array branch unwrapped
	out, _ = ConformToSchema(`{"u":"[\"a\"]"}`, sch)
	if gjson.Get(out, "u.0").String() != "a" {
		t.Fatalf("got %s", out)
	}
}
