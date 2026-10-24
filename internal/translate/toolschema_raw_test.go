package translate

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"

	"claudgravity/internal/config"
	"claudgravity/internal/schema"
)

func rawMode(c *config.TenantConfig) { c.UseParametersJsonSchema = true }

func toolReq(inputSchema string) string {
	return `{"model":"gemini-3.8-flash","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"T","description":"d","input_schema":` + inputSchema + `}]}`
}

const richSchema = `{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","$defs":{"P":{"type":"object","properties":{"x":{"type":"integer","minimum":3}}}},` +
	`"properties":{"p":{"$ref":"#/$defs/P"},"v":{"anyOf":[{"type":"string","minLength":2},{"type":"integer"}]},"k":{"const":"a"},"n":{"type":"string","pattern":"^a+$"}},` +
	`"required":["v"],"additionalProperties":false}`

func firstDecl(t *testing.T, res Result) gjson.Result {
	t.Helper()
	d := gjson.GetBytes(res.GeminiBody, "tools.0.functionDeclarations.0")
	if !d.Exists() {
		t.Fatalf("no function declaration in %s", res.GeminiBody)
	}
	return d
}

func TestRawParametersSchema_SentUntouchedUnderNewKey(t *testing.T) {
	res, err := convertTools(t, toolReq(richSchema), "gemini-3.8-flash", rawMode)
	if err != nil {
		t.Fatal(err)
	}
	d := firstDecl(t, res)
	if d.Get("parameters").Exists() {
		t.Fatalf("raw mode must not also emit `parameters`: %s", d.Raw)
	}
	got := d.Get("parametersJsonSchema")
	if got.Raw != richSchema {
		t.Fatalf("schema altered:\n got %s\nwant %s", got.Raw, richSchema)
	}
}

func TestRawParametersSchema_OffByDefaultKeepsCleanedParameters(t *testing.T) {
	res, err := convertTools(t, toolReq(richSchema), "gemini-3.8-flash", nil)
	if err != nil {
		t.Fatal(err)
	}
	d := firstDecl(t, res)
	if d.Get("parametersJsonSchema").Exists() || !d.Get("parameters").Exists() {
		t.Fatalf("default must stay on cleaned `parameters`: %s", d.Raw)
	}
	if strings.Contains(d.Get("parameters").Raw, `"$ref"`) || strings.Contains(d.Get("parameters").Raw, `"const"`) {
		t.Fatalf("cleaner left unsupported keywords: %s", d.Get("parameters").Raw)
	}
}

func TestRawParametersSchema_EmptySchemaStillGetsPlaceholder(t *testing.T) {
	for _, sc := range []string{`{}`, `{"type":"object","properties":{}}`} {
		res, err := convertTools(t, toolReq(sc), "gemini-3.8-flash", rawMode)
		if err != nil {
			t.Fatal(err)
		}
		p := firstDecl(t, res).Get("parametersJsonSchema")
		if !p.Get("properties.reason").Exists() {
			t.Fatalf("schema %s: want synthetic reason placeholder, got %s", sc, p.Raw)
		}
		if !res.SyntheticReasonTools["T"] {
			t.Fatalf("placeholder not recorded for stripping: %+v", res.SyntheticReasonTools)
		}
	}
}

func TestRawParametersSchema_ScalarRootWrappedWithDefsHoisted(t *testing.T) {
	sc := `{"type":"array","items":{"$ref":"#/$defs/I"},"$defs":{"I":{"type":"string"}}}`
	res, err := convertTools(t, toolReq(sc), "gemini-3.8-flash", rawMode)
	if err != nil {
		t.Fatal(err)
	}
	p := firstDecl(t, res).Get("parametersJsonSchema")
	if p.Get("type").String() != "object" || p.Get("required.0").String() != "__scalar_arg__" {
		t.Fatalf("not wrapped: %s", p.Raw)
	}
	if !p.Get(`$defs.I`).Exists() || p.Get(`properties.__scalar_arg__.$defs`).Exists() {
		t.Fatalf("$defs must live at the new root so #/$defs/I resolves: %s", p.Raw)
	}
	if p.Get(`properties.__scalar_arg__.items.$ref`).String() != "#/$defs/I" {
		t.Fatalf("reference lost: %s", p.Raw)
	}
}

func TestRawParametersSchema_CompositionRootIsNotReplacedByPlaceholder(t *testing.T) {
	sc := `{"anyOf":[{"type":"object","properties":{"a":{"type":"string"}}},{"type":"object","properties":{"b":{"type":"integer"}}}]}`
	res, err := convertTools(t, toolReq(sc), "gemini-3.8-flash", rawMode)
	if err != nil {
		t.Fatal(err)
	}
	p := firstDecl(t, res).Get("parametersJsonSchema")
	if p.Get("properties.reason").Exists() || !p.Get("properties.__scalar_arg__.anyOf").Exists() {
		t.Fatalf("anyOf root lost: %s", p.Raw)
	}
}

func TestRawParametersSchema_ArgumentsStillConformedToDeclaredSchema(t *testing.T) {
	sc := `{"type":"object","properties":{"count":{"type":"integer"},"flag":{"type":"boolean"}}}`
	if _, err := convertTools(t, toolReq(sc), "gemini-3.8-flash", rawMode); err != nil {
		t.Fatal(err)
	}
	got := schema.ConformToolArgs("sess-"+t.Name(), "T", `{"count":"7","flag":"true"}`)
	if gjson.Get(got, "count").Type != gjson.Number || gjson.Get(got, "flag").Type != gjson.True {
		t.Fatalf("args not conformed to the recorded schema: %s", got)
	}
}
