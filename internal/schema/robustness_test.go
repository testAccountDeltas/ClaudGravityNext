package schema

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

func TestWrapBareScalarRootRoundTrip(t *testing.T) {
	for _, in := range []string{`{"type":"string","description":"d"}`, `{"type":"integer"}`, `{"type":"array","items":{"type":"string"}}`} {
		out := wrapBareScalarRoot(in)
		if gjson.Get(out, "type").String() != "object" || gjson.Get(out, "required.0").String() != "__scalar_arg__" {
			t.Fatalf("not wrapped: %s", out)
		}
		if gjson.Get(out, "properties.__scalar_arg__").Raw != in {
			t.Fatalf("inner schema altered: %s", out)
		}
	}
	// objects and schemas with properties are returned untouched
	for _, in := range []string{`{"type":"object","properties":{"a":{"type":"string"}}}`, `{"properties":{"a":{}}}`, `{}`} {
		if out := wrapBareScalarRoot(in); out != in {
			t.Fatalf("must not wrap %s -> %s", in, out)
		}
	}
	// via the public entry: wrapped once, never twice
	out := CleanJSONSchemaForAntigravity("s-wrap", "T", `{"type":"string"}`)
	if gjson.Get(out, "properties.__scalar_arg__.type").String() != "string" || gjson.Get(out, "properties.__scalar_arg__.properties").Exists() {
		t.Fatalf("double wrapped: %s", out)
	}
}

func TestPlaceholderRoundTrip(t *testing.T) {
	in := `{"type":"object","properties":{"env":{"type":"object"},"name":{"type":"string"},"nested":{"type":"object","properties":{}}}}`
	withPH, fields := AddEmptySchemaPlaceholder(in, nil)
	if gjson.Get(withPH, "properties.env.properties.reason.type").String() != "string" ||
		gjson.Get(withPH, "properties.nested.properties.reason.type").String() != "string" ||
		gjson.Get(withPH, "properties.env.required.0").String() != "reason" {
		t.Fatalf("placeholders missing: %s", withPH)
	}
	if len(fields) != 2 {
		t.Fatalf("want 2 open-dict fields, got %v", fields)
	}
	if gjson.Get(withPH, "properties.name.properties").Exists() {
		t.Fatalf("scalar property must not get a placeholder: %s", withPH)
	}
	back := removePlaceholderFields(withPH)
	if gjson.Get(back, "properties.env.properties.reason").Exists() || gjson.Get(back, "properties.env.required").Exists() ||
		gjson.Get(back, "properties.nested.properties.reason").Exists() {
		t.Fatalf("placeholders not removed: %s", back)
	}
	if gjson.Get(back, "properties.name.type").String() != "string" {
		t.Fatalf("real property lost: %s", back)
	}
	// a real property named reason with its own description survives
	real := `{"type":"object","properties":{"reason":{"type":"string","description":"why"},"x":{"type":"string"}},"required":["reason"]}`
	if out := removePlaceholderFields(real); out != real {
		t.Fatalf("real reason property removed: %s", out)
	}
	// the root-empty case
	root, _ := AddEmptySchemaPlaceholder(`{}`, nil)
	if !HasTopLevelPlaceholderReason(root) || HasTopLevelPlaceholderReason(real) {
		t.Fatalf("HasTopLevelPlaceholderReason wrong for %s / %s", root, real)
	}
	if out := removePlaceholderFields(root); gjson.Get(out, "properties.reason").Exists() {
		t.Fatalf("root placeholder not removed: %s", out)
	}
}

func TestAnyOfSameKeyDifferentTypesNotDropped(t *testing.T) {
	in := `{"type":"object","properties":{"v":{"anyOf":[
		{"type":"object","properties":{"id":{"type":"string","description":"string id"},"a":{"type":"string"}},"required":["id"]},
		{"type":"object","properties":{"id":{"type":"integer"},"b":{"type":"string"}},"required":["id"]}]}}}`
	out := CleanJSONSchemaForGemini(in)
	id := gjson.Get(out, "properties.v.properties.id")
	if !id.Exists() || !gjson.Get(out, "properties.v.properties.a").Exists() || !gjson.Get(out, "properties.v.properties.b").Exists() {
		t.Fatalf("merged properties missing: %s", out)
	}
	if !strings.Contains(strings.ToLower(id.Raw), "integer") {
		t.Fatalf("type of the second branch silently dropped for id: %s", id.Raw)
	}
}

func TestRemoveKeywords(t *testing.T) {
	in := `{"type":"object","title":"T","properties":{"title":{"type":"string","title":"x"},"n":{"type":"integer","examples":[1],"default":3}},"examples":[{}]}`
	out := removeKeywords(in, []string{"title", "examples", "default"})
	if gjson.Get(out, "title").Exists() || gjson.Get(out, "examples").Exists() || gjson.Get(out, "properties.n.default").Exists() ||
		gjson.Get(out, "properties.n.examples").Exists() || gjson.Get(out, "properties.title.title").Exists() {
		t.Fatalf("keywords left: %s", out)
	}
	if gjson.Get(out, "properties.title.type").String() != "string" {
		t.Fatalf("a property literally named like a keyword must survive: %s", out)
	}
	if out := removeKeywords(in, nil); out != in {
		t.Fatal("no keywords must be a no-op")
	}
}

func TestMutualRefCycleTerminates(t *testing.T) {
	in := `{"type":"object","properties":{"a":{"$ref":"#/$defs/A"}},"$defs":{"A":{"type":"object","properties":{"b":{"$ref":"#/$defs/B"}}},"B":{"type":"object","properties":{"a":{"$ref":"#/$defs/A"}}}}}`
	done := make(chan string, 1)
	go func() { done <- CleanJSONSchemaForAntigravity("s-cyc", "Cyc", in) }()
	select {
	case out := <-done:
		if !gjson.Valid(out) || strings.Contains(out, `"$ref"`) {
			t.Fatalf("cycle output invalid or still holds $ref: %s", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("mutual $ref cycle did not terminate")
	}
}

func timed(t *testing.T, name string, limit time.Duration, f func() string) string {
	t.Helper()
	start := time.Now()
	out := f()
	if d := time.Since(start); d > limit {
		t.Fatalf("%s took %v (limit %v)", name, d, limit)
	}
	if !gjson.Valid(out) {
		t.Fatalf("%s: invalid JSON out", name)
	}
	return out
}

func TestHugeSchemaWithinTimeBound(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"type":"object","properties":{`)
	for i := 0; i < 5000; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"p%d":{"type":"string","description":"d%d","minLength":1}`, i, i)
	}
	sb.WriteString(`}}`)
	out := timed(t, "5000 properties", 60*time.Second, func() string { return CleanJSONSchemaForGemini(sb.String()) })
	if n := len(gjson.Get(out, "properties").Map()); n != 5000 {
		t.Fatalf("properties %d, want 5000", n)
	}
}

func TestDeepAnyOfWithinTimeBound(t *testing.T) {
	inner := `{"type":"string"}`
	for i := 0; i < 200; i++ {
		inner = fmt.Sprintf(`{"anyOf":[%s,{"type":"integer"}]}`, inner)
	}
	in := `{"type":"object","properties":{"x":` + inner + `}}`
	timed(t, "200-deep anyOf", 30*time.Second, func() string { return CleanJSONSchemaForGemini(in) })
}
