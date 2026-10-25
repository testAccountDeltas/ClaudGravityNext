package jsondiff

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func decode(t *testing.T, s string) interface{} {
	t.Helper()
	var v interface{}
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func diff(t *testing.T, a, b string) []string {
	t.Helper()
	var out []string
	Diff("", decode(t, a), decode(t, b), &out)
	sort.Strings(out)
	return out
}

func TestDiff(t *testing.T) {
	for _, c := range []struct {
		name, a, b string
		want       []string
	}{
		{"equal", `{"a":[1,{"b":"x"}],"c":null}`, `{"c":null,"a":[1,{"b":"x"}]}`, nil},
		{"leaf value", `{"a":{"b":1}}`, `{"a":{"b":2}}`, []string{"/a/b"}},
		{"missing key", `{"a":1,"b":2}`, `{"a":1}`, []string{"/b (missing in v2)"}},
		{"extra key", `{"a":1}`, `{"a":1,"b":2}`, []string{"/b (extra in v2)"}},
		{"object vs array", `{"a":{}}`, `{"a":[]}`, []string{"/a (type: object vs array)"}},
		{"array vs string", `{"a":[]}`, `{"a":"s"}`, []string{"/a (type: array vs string)"}},
		{"object vs number", `{"a":{}}`, `{"a":1}`, []string{"/a (type: object vs number)"}},
		{"object vs bool", `{"a":{}}`, `{"a":true}`, []string{"/a (type: object vs bool)"}},
		{"object vs null", `{"a":{}}`, `{"a":null}`, []string{"/a (type: object vs null)"}},
		{"array length and common prefix", `{"a":[1,2,3]}`, `{"a":[1,9]}`, []string{"/a (array length 3 vs 2)", "/a/1"}},
		{"scalar type change", `{"a":1}`, `{"a":"1"}`, []string{"/a"}},
		{"pointer escaping", `{"a/b":1,"c~d":1}`, `{"a/b":2,"c~d":2}`, []string{"/a~1b", "/c~0d"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := diff(t, c.a, c.b)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestDiff_UnknownGoTypeIsNamedByItsType(t *testing.T) {
	var out []string
	Diff("", map[string]interface{}{}, 42, &out)
	if len(out) != 1 || !strings.Contains(out[0], "vs int") {
		t.Fatalf("got %q", out)
	}
}

func TestIsStructural(t *testing.T) {
	for p, want := range map[string]bool{
		"/a (missing in v2)":           true,
		"/a (extra in v2)":             true,
		"/a (type: object vs array)":   true,
		"/a (array length 1 vs 2)":     true,
		"/a/b":                         false,
		"/a (something else entirely)": false,
	} {
		if got := IsStructural(p); got != want {
			t.Errorf("IsStructural(%q) = %v", p, got)
		}
	}
}

func TestRedact(t *testing.T) {
	long := strings.Repeat("x", RedactThreshold+1)
	exact := strings.Repeat("y", RedactThreshold)
	in := decode(t, `{"k":"`+long+`","keep":"`+exact+`","n":[1,true,null,{"deep":"`+long+`"}]}`)
	got := Redact(in).(map[string]interface{})
	if got["k"] != "<redacted text:201 chars>" {
		t.Fatalf("long string not redacted: %v", got["k"])
	}
	if got["keep"] != exact {
		t.Fatal("a string at exactly the threshold must be kept")
	}
	arr := got["n"].([]interface{})
	if arr[0] != float64(1) || arr[1] != true || arr[2] != nil {
		t.Fatalf("non-strings must pass through: %v", arr)
	}
	if arr[3].(map[string]interface{})["deep"] != "<redacted text:201 chars>" {
		t.Fatal("redaction must reach nested values")
	}
	if in.(map[string]interface{})["k"] != long {
		t.Fatal("Redact must not mutate its input")
	}
}
