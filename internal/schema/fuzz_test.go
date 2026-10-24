package schema

import (
	"testing"

	"github.com/tidwall/gjson"
)

func FuzzCleanSchema(f *testing.F) {
	seeds := []string{
		`{"type":"object","properties":{"a":{"type":"string"}}}`,
		`{"type":"object","properties":{"v":{"anyOf":[{"type":"object","properties":{"id":{"type":"string"}}},{"type":"object","properties":{"id":{"type":"integer"}}}]}}}`,
		`{"type":"string"}`, `{}`, `{"$ref":"#/$defs/A","$defs":{"A":{"$ref":"#/$defs/A"}}}`,
		`{"type":"object","properties":{"e":{"enum":[1,2,"x"]},"c":{"const":3},"t":{"type":["string","null"]}},"required":["e","zz"]}`,
		`{"allOf":[{"type":"object","properties":{"a":{"type":"string"}}},{"properties":{"b":{"type":"number"}}}]}`,
		`{"type":"object","properties":{"env":{"type":"object","additionalProperties":{"type":"string"}}}}`,
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if !gjson.Valid(in) {
			return
		}
		if out := CleanJSONSchemaForGemini(in); !gjson.Valid(out) {
			t.Fatalf("CleanJSONSchemaForGemini(%q) = %q invalid", in, out)
		}
		if out := CleanJSONSchemaForAntigravity("fz", "", in); !gjson.Valid(out) {
			t.Fatalf("CleanJSONSchemaForAntigravity(%q) = %q invalid", in, out)
		}
	})
}
