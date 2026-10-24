package toolargs

import (
	"encoding/json"
	"testing"
)

var argSeeds = []string{
	`{"command":"ls -la"}`, `"ls -la"`, `"{\"cmd\":\"ls\"}"`, `{"args":"{\"a\":1}"}`,
	`{"__scalar_arg__":"x"}`, `{"__scalar_arg__":{"a":1}}`, `{"__scalar_arg__":"[1,2]"}`,
	"{\"c\":\"a\nb\"}", `null`, `[]`, `12`, ``, `{`, `{"a":12345678901234567890}`,
}

func FuzzNormalizeToArgsObject(f *testing.F) {
	for _, s := range argSeeds {
		f.Add("Bash", s)
		f.Add("unknown_tool", s)
	}
	f.Fuzz(func(t *testing.T, name, raw string) {
		out := NormalizeToArgsObject(name, raw)
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(out), &m); err != nil || m == nil {
			t.Fatalf("NormalizeToArgsObject(%q,%q) = %q is not a JSON object", name, raw, out)
		}
	})
}

func FuzzConform(f *testing.F) {
	schema := `{"type":"object","properties":{"a":{"type":"integer"},"b":{"type":["string","null"]},"c":{"anyOf":[{"type":"array","items":{"type":"number"}},{"type":"object"}]},"d":{"$ref":"#/$defs/D"}},"required":["a"],"$defs":{"D":{"type":"object","properties":{"e":{"enum":["x","y"]}}}}}`
	for _, s := range argSeeds {
		f.Add(s, schema)
	}
	f.Add(`{"a":"5","b":null,"c":"[1]","d":{"e":"z"}}`, schema)
	f.Add(`{"a":1}`, `{"$ref":"#"}`)
	f.Add(`{"a":1}`, `{"type":"object","properties":{"a":{"$ref":"#/properties/a"}}}`)
	f.Fuzz(func(t *testing.T, args, schema string) {
		out, _ := ConformToSchema(args, schema)
		if json.Valid([]byte(args)) && !json.Valid([]byte(out)) {
			t.Fatalf("ConformToSchema(%q,%q) = %q is invalid JSON", args, schema, out)
		}
		if !json.Valid([]byte(args)) && out != args {
			t.Fatalf("invalid input must come back unchanged: %q -> %q", args, out)
		}
	})
}
