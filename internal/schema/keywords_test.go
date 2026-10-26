package schema

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestMoveConstraints_SkipsTheJavaScriptSafeIntegerBounds(t *testing.T) {
	in := `{"type":"object","properties":{"n":{"type":"integer","description":"count","minimum":-9007199254740991,"maximum":9007199254740991},"m":{"type":"integer","description":"size","maximum":100}}}`
	out := moveConstraintsToDescription(in)
	if got := gjson.Get(out, "properties.n.description").String(); got != "count" {
		t.Fatalf("Zod's default bounds must not clutter the description: %q", got)
	}
	if got := gjson.Get(out, "properties.m.description").String(); !strings.Contains(got, "maximum: 100") {
		t.Fatalf("a real bound must still be kept: %q", got)
	}
}
