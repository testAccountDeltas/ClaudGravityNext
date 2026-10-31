package toolargs

import "testing"

// Found by FuzzConform: "{}0" is not valid JSON, so it must come back
// unchanged instead of being reduced to "{}".
func TestConformToSchema_TrailingDataLeftUntouched(t *testing.T) {
	schema := `{"type":"object","properties":{"a":{"type":"integer"}}}`
	for _, args := range []string{`{}0`, `{"a":"5"} x`, `{"a":"5"}{"b":1}`} {
		if out, _ := ConformToSchema(args, schema); out != args {
			t.Errorf("ConformToSchema(%q) = %q, want it unchanged", args, out)
		}
	}
	if out, _ := ConformToSchema(` {"a":"5"}  `, schema); out == "" {
		t.Error("surrounding whitespace must still be accepted")
	}
}
