package schema

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestMixedUnion_StringOrArray_KeepsInformationAndRoundTrips(t *testing.T) {
	in := `{"type":"object","properties":{"files":{"description":"Files","anyOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]},"n":{"anyOf":[{"type":"integer"},{"type":"null"}]}},"required":["files"]}`
	out := CleanJSONSchemaForAntigravity("s_mixed", "MixTool", in)
	if gjson.Get(out, "properties.files.type").String() != "string" {
		t.Fatalf("mixed union must be declared string: %s", out)
	}
	desc := gjson.Get(out, "properties.files.description").String()
	if !containsSub(desc, "array of string") || !containsSub(desc, "Files") {
		t.Fatalf("description must enumerate forms and keep original: %q", desc)
	}
	// a single non-null kind (integer|null) is NOT degraded
	if gjson.Get(out, "properties.n.type").String() != "integer" {
		t.Fatalf("integer|null must stay integer: %s", out)
	}
	args := CoerceNumericEnumArgs("s_mixed", "MixTool", `{"files":"[\"a\",\"b\"]","n":"7"}`)
	if !gjson.Get(args, "files").IsArray() || gjson.Get(args, "n").Type != gjson.Number {
		t.Fatalf("response path must restore declared shapes: %s", args)
	}
}

func TestConformToolArgs_StringFieldsAndNulls(t *testing.T) {
	in := `{"type":"object","properties":{"file_path":{"type":"string"},"pages":{"type":"string"},"limit":{"type":"integer"}},"required":["file_path"]}`
	CleanJSONSchemaForAntigravity("s_read", "Read", in)
	out := CoerceNumericEnumArgs("s_read", "Read", `{"file_path":"a.pdf","pages":"3","limit":"10","offset":null}`)
	if gjson.Get(out, "pages").Type != gjson.String || gjson.Get(out, "limit").Int() != 10 || gjson.Get(out, "offset").Exists() {
		t.Fatalf("%s", out)
	}
}

func TestConformToolArgs_UnknownToolOnlyTopLevelNullsPruned(t *testing.T) {
	out := ConformToolArgs("s_none", "Nope", `{"a":null,"b":{"c":null},"d":"3"}`)
	if gjson.Get(out, "a").Exists() || !gjson.Get(out, "b.c").Exists() || gjson.Get(out, "d").Type != gjson.String {
		t.Fatalf("%s", out)
	}
}

func containsSub(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
