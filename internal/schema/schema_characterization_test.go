// Characterization tests for internal/schema against v1's gemini_schema.go.
// One test per audit-caught edge case documented in that file's own
// comments, focused on the riskiest/most-restructured areas (anyOf/oneOf
// merging) plus one test per other file's primary behavior.
package schema

import (
	"testing"

	"github.com/tidwall/gjson"
)

func mustSchema(t *testing.T, raw string) string {
	t.Helper()
	if !gjson.Valid(raw) {
		t.Fatalf("invalid JSON literal in test: %s", raw)
	}
	return raw
}

// GAP-S5: a root-level "const" (parentPath=="") used to produce the
// literal path ".enum" (a leading-dot string-concat bug), so sjson wrote
// a bogus top-level key named ".enum" instead of "enum", leaving the real
// "enum"/"type" keys this schema's own consumers look for absent.
func TestConvertConstToEnum_RootLevelConst_SetsRealEnumAndTypeKeys(t *testing.T) {
	in := mustSchema(t, `{"const":"fixed-value"}`)
	out := convertConstToEnum(in)
	if gjson.Get(out, "enum.0").String() != "fixed-value" {
		t.Fatalf("expected root enum.0=fixed-value, got %s", out)
	}
	if gjson.Get(out, "type").String() != "string" {
		t.Fatalf("expected root type=string, got %s", out)
	}
	if gjson.Get(out, ".enum").Exists() {
		t.Fatalf("must not create a literal \".enum\" key, got %s", out)
	}
}

// GAP-S9: a node declared only via "patternProperties" (no fixed
// "properties" of its own) is a free-form dict exactly like the
// additionalProperties-only "env" case TestCleanJSONSchema_
// OpenDictWithNoTypeOfItsOwn_BecomesObject already covers вЂ” it must get
// the same "type":"object" forced in, or it reaches Gemini as a bare {}
// ("Schema cannot be empty") instead of the placeholder-then-strip
// treatment every other empty-object node gets.
func TestCleanJSONSchema_PatternPropertiesWithNoTypeOfItsOwn_BecomesObject(t *testing.T) {
	in := mustSchema(t, `{"type":"object","properties":{"headers":{"patternProperties":{"^x-":{"type":"string"}}}}}`)
	out := CleanJSONSchemaForAntigravity("sess1", "MyTool", in)
	if gjson.Get(out, "properties.headers.type").String() != "object" {
		t.Fatalf("expected patternProperties field forced to type object, got %s", out)
	}
}

// --- clean.go / orchestration ---

func TestCleanJSONSchemaForAntigravity_EmptyObject_GetsReasonPlaceholder(t *testing.T) {
	in := mustSchema(t, `{"type":"object","properties":{}}`)
	out := CleanJSONSchemaForAntigravity("sess1", "MyTool", in)
	if gjson.Get(out, "properties.reason.type").String() != "string" {
		t.Fatalf("expected a reason placeholder, got %s", out)
	}
	req := gjson.Get(out, "required").Array()
	if len(req) != 1 || req[0].String() != "reason" {
		t.Fatalf("expected required=[reason], got %s", out)
	}
}

func TestCleanJSONSchemaForGemini_NoPlaceholder(t *testing.T) {
	in := mustSchema(t, `{"type":"object","properties":{}}`)
	out := CleanJSONSchemaForGemini(in)
	if gjson.Get(out, "properties.reason").Exists() {
		t.Fatalf("Structured Outputs path must never add the reason placeholder, got %s", out)
	}
}

func TestCleanJSONSchema_RootWithPropertiesButNoType_GetsObjectType(t *testing.T) {
	in := mustSchema(t, `{"properties":{"x":{"type":"string"}}}`)
	out := CleanJSONSchemaForGemini(in)
	if gjson.Get(out, "type").String() != "object" {
		t.Fatalf("expected root type forced to object, got %s", out)
	}
}

func TestCleanJSONSchema_OpenDictWithNoTypeOfItsOwn_BecomesObject(t *testing.T) {
	in := mustSchema(t, `{"type":"object","properties":{"env":{"additionalProperties":{"type":"string"}}}}`)
	out := CleanJSONSchemaForAntigravity("sess1", "Bash", in)
	if gjson.Get(out, "properties.env.type").String() != "object" {
		t.Fatalf("expected open-dict field forced to type object, got %s", out)
	}
}

// --- enums.go ---

func TestConvertEnumValuesToStrings_NullEntry_SetsNullableDropsNull(t *testing.T) {
	out := convertEnumValuesToStrings(`{"enum":["a","b",null]}`)
	if !gjson.Get(out, "nullable").Bool() {
		t.Fatalf("expected nullable=true, got %s", out)
	}
	vals := gjson.Get(out, "enum").Array()
	for _, v := range vals {
		if v.String() == "" {
			t.Fatalf("null must not become an empty-string enum entry, got %s", out)
		}
	}
}

func TestConvertEnumValuesToStrings_PurelyNullEnum_DeletesEnumKey(t *testing.T) {
	// GAP-SC2: "null" is not a valid Gemini Schema type вЂ” a purely-null
	// enum must become "string"+"nullable":true, the same real mechanism
	// used for a mixed enum's null entry just below, not a literal
	// "type":"null" Gemini's protobuf descriptor has no slot for.
	out := convertEnumValuesToStrings(`{"enum":[null]}`)
	if gjson.Get(out, "enum").Exists() {
		t.Fatalf("a purely-null enum must be deleted, not written back as [], got %s", out)
	}
	if gjson.Get(out, "type").String() != "string" {
		t.Fatalf("expected type=string, got %s", out)
	}
	if !gjson.Get(out, "nullable").Bool() {
		t.Fatalf("expected nullable=true, got %s", out)
	}
}

func TestConvertConstToEnum_NumericConst_InfersIntegerType(t *testing.T) {
	// Nested under a property, not at the schema root: convertConstToEnum
	// builds its enum/type paths via plain string concatenation
	// (parentPath+".enum"), which is only valid once parentPath is
	// non-empty вЂ” the bare-root case is a pre-existing v1 quirk this
	// faithful port carries forward unchanged, not something to paper
	// over in the test.
	out := convertConstToEnum(`{"type":"object","properties":{"x":{"const":1}}}`)
	if gjson.Get(out, "properties.x.type").String() != "integer" {
		t.Fatalf("expected inferred type integer, got %s", out)
	}
	if gjson.Get(out, "properties.x.enum.0").Int() != 1 {
		t.Fatalf("got %s", out)
	}
}

// --- allof.go ---

func TestMergeAllOf_TwoBranchesCollideOnNestedRequired_Unions(t *testing.T) {
	in := `{"allOf":[
		{"properties":{"user":{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}}},
		{"properties":{"user":{"type":"object","properties":{"email":{"type":"string"}},"required":["email"]}}}
	]}`
	out := mergeAllOf(in)
	req := gjson.Get(out, "properties.user.required").Array()
	var got []string
	for _, r := range req {
		got = append(got, r.String())
	}
	if len(got) != 2 || !contains(got, "name") || !contains(got, "email") {
		t.Fatalf("expected union [name email], got %v", got)
	}
}

func TestMergeAllOf_PreservesSiblingOneOf(t *testing.T) {
	in := `{"allOf":[{"type":"string"},{"oneOf":[{"const":"a"},{"const":"b"}]}]}`
	out := mergeAllOf(in)
	if !gjson.Get(out, "oneOf").IsArray() {
		t.Fatalf("allOf's sibling oneOf must survive the merge, got %s", out)
	}
}

// --- anyof.go / anyof_merge.go (the most heavily restructured area) ---

func TestFlattenAnyOfOneOf_MultipleObjectBranches_MergesNotPicksFirst(t *testing.T) {
	in := `{"oneOf":[
		{"type":"object","properties":{"action":{"const":"create"},"data":{"type":"string"}},"required":["action","data"]},
		{"type":"object","properties":{"action":{"const":"delete"},"id":{"type":"string"}},"required":["action","id"]}
	]}`
	out := flattenAnyOfOneOf(in, true)
	if !gjson.Get(out, "properties.data").Exists() || !gjson.Get(out, "properties.id").Exists() {
		t.Fatalf("expected both branches' own fields merged in, got %s", out)
	}
	actionEnum := gjson.Get(out, "properties.action.enum").Array()
	var vals []string
	for _, v := range actionEnum {
		vals = append(vals, v.String())
	}
	if len(vals) != 2 || !contains(vals, "create") || !contains(vals, "delete") {
		t.Fatalf("expected action enum [create delete], got %v from %s", vals, out)
	}
	// "action" is required by BOTH branches (the discriminator) so it
	// survives the intersection; "data"/"id" are each required by only
	// ONE branch so neither does.
	req := gjson.Get(out, "required").Array()
	var reqVals []string
	for _, r := range req {
		reqVals = append(reqVals, r.String())
	}
	if len(reqVals) != 1 || reqVals[0] != "action" {
		t.Fatalf("expected required=[action] (intersection), got %v from %s", reqVals, out)
	}
}

func TestFlattenAnyOfOneOf_FreeScalarSibling_StripsEnum(t *testing.T) {
	in := `{"oneOf":[{"type":"string","enum":["a","b"]},{"type":"string"}]}`
	out := flattenAnyOfOneOf(in, true)
	if gjson.Get(out, "enum").Exists() {
		t.Fatalf("a free-scalar sibling must strip the enum entirely, got %s", out)
	}
}

func TestFlattenAnyOfOneOf_NullBranch_MarksNullableAndStripsRequired(t *testing.T) {
	in := `{"type":"object","properties":{"x":{"oneOf":[{"type":"string"},{"type":"null"}]}},"required":["x"]}`
	out := flattenAnyOfOneOf(in, true)
	if !gjson.Get(out, "properties.x.nullable").Bool() {
		t.Fatalf("expected properties.x.nullable=true, got %s", out)
	}
	req := gjson.Get(out, "required")
	if req.Exists() {
		for _, r := range req.Array() {
			if r.String() == "x" {
				t.Fatalf("a nullable field must be stripped from required when addPlaceholder=true, got %s", out)
			}
		}
	}
}

func TestFlattenAnyOfOneOf_NullBranch_KeepsRequiredWhenNotAddPlaceholder(t *testing.T) {
	// Structured Outputs: required means "key present", independent of
	// nullable ("value may be null") вЂ” must NOT strip required here.
	in := `{"type":"object","properties":{"x":{"oneOf":[{"type":"string"},{"type":"null"}]}},"required":["x"]}`
	out := flattenAnyOfOneOf(in, false)
	req := gjson.Get(out, "required").Array()
	found := false
	for _, r := range req {
		if r.String() == "x" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected required=[x] preserved for Structured Outputs, got %s", out)
	}
}

func TestFlattenAnyOfOneOf_PreservesSiblingBaseProperty(t *testing.T) {
	// A base "action" field sitting alongside oneOf must survive a full
	// splice, not get destroyed by a naive setRawAt replace.
	in := `{"type":"object","properties":{"action":{"type":"string"}},"oneOf":[{"type":"object","properties":{"value":{"type":"string"}}}]}`
	out := flattenAnyOfOneOf(in, true)
	if !gjson.Get(out, "properties.action").Exists() {
		t.Fatalf("expected sibling property 'action' preserved, got %s", out)
	}
	if !gjson.Get(out, "properties.value").Exists() {
		t.Fatalf("expected oneOf branch's own property merged in, got %s", out)
	}
}

func TestMergeObjectBranches_NestedRequiredIsIntersectionNotUnion(t *testing.T) {
	branches := []gjson.Result{
		gjson.Parse(`{"type":"object","properties":{"config":{"type":"object","properties":{"create_token":{"type":"string"}},"required":["create_token"]}}}`),
		gjson.Parse(`{"type":"object","properties":{"config":{"type":"object","properties":{"delete_token":{"type":"string"}},"required":["delete_token"]}}}`),
	}
	out := mergeObjectBranches(branches)
	if gjson.Get(out, "properties.config.required").Exists() {
		t.Fatalf("expected no nested required to survive the intersection (each token is branch-specific), got %s", out)
	}
	if !gjson.Get(out, "properties.config.properties.create_token").Exists() || !gjson.Get(out, "properties.config.properties.delete_token").Exists() {
		t.Fatalf("expected both branches' own config sub-fields merged in, got %s", out)
	}
}

func TestSelectBest_EnumBranchWithLeadingNull_InfersRealScalarType(t *testing.T) {
	items := []gjson.Result{gjson.Parse(`{"enum":[null,1,2]}`)}
	_, types := selectBest(items)
	if len(types) != 1 || types[0] != "number" {
		t.Fatalf("expected [number] (first non-null element wins), got %v", types)
	}
}

func TestSelectBest_PurelyNullEnum_ScoresLikeExplicitNull(t *testing.T) {
	items := []gjson.Result{
		gjson.Parse(`{"enum":[null]}`),
		gjson.Parse(`{"type":"string"}`),
	}
	bestIdx, types := selectBest(items)
	if bestIdx != 1 {
		t.Fatalf("a purely-null enum branch must never outrank a real typed sibling, got bestIdx=%d types=%v", bestIdx, types)
	}
}

// --- numericenum.go ---

func TestRecordAndCoerceNumericEnumArgs_RoundTripsIntegerEnum(t *testing.T) {
	schema := `{"properties":{"priority":{"type":"integer","enum":[1,2,3]}}}`
	RecordNumericEnumFields("sess1", "SetPriority", schema)
	args := `{"priority":"2"}`
	out := CoerceNumericEnumArgs("sess1", "SetPriority", args)
	if gjson.Get(out, "priority").Type != gjson.Number || gjson.Get(out, "priority").Int() != 2 {
		t.Fatalf("expected priority coerced back to int 2, got %s", out)
	}
}

func TestRecordAndCoerceNumericEnumArgs_ArrayOfIntegerEnum(t *testing.T) {
	schema := `{"properties":{"ports":{"type":"array","items":{"type":"integer","enum":[80,443]}}}}`
	RecordNumericEnumFields("sess1", "OpenPorts", schema)
	args := `{"ports":["80","443"]}`
	out := CoerceNumericEnumArgs("sess1", "OpenPorts", args)
	if gjson.Get(out, "ports.0").Int() != 80 || gjson.Get(out, "ports.1").Int() != 443 {
		t.Fatalf("got %s", out)
	}
}

// --- tuples.go ---

func TestConvertDraft7TuplesToItems_MultiElement_BecomesOneOf(t *testing.T) {
	out := convertDraft7TuplesToItems(`{"items":[{"type":"string"},{"type":"number"}]}`)
	if !gjson.Get(out, "items.oneOf").IsArray() {
		t.Fatalf("expected items to become a oneOf of the original elements, got %s", out)
	}
}

func TestConvertPrefixItemsToItems_ItemsFalse_DoesNotBlockConversion(t *testing.T) {
	// "items": false with prefixItems means "no items beyond the tuple",
	// not "items already has a real schema" вЂ” must not block conversion.
	out := convertPrefixItemsToItems(`{"prefixItems":[{"type":"string"}],"items":false}`)
	if gjson.Get(out, "items").Type == gjson.False {
		t.Fatalf("expected prefixItems to be converted despite items:false, got %s", out)
	}
}

// --- placeholders.go ---

func TestStripNestedOpenDictReason_RemovesNestedPlaceholder(t *testing.T) {
	schemaRaw := `{"type":"object","properties":{"env":{"type":"object","properties":{}}}}`
	cleaned := CleanJSONSchemaForAntigravity("sess1", "Bash", schemaRaw)
	_ = cleaned // registers "env" in openDictArgFields via addEmptySchemaPlaceholder's nested path

	args := `{"env":{"reason":"explaining","FOO":"bar"}}`
	out := StripNestedOpenDictReason("sess1", "Bash", args)
	if gjson.Get(out, "env.reason").Exists() {
		t.Fatalf("expected nested reason placeholder stripped, got %s", out)
	}
	if gjson.Get(out, "env.FOO").String() != "bar" {
		t.Fatalf("real nested fields must survive, got %s", out)
	}
}

// --- required.go ---

func TestFlattenTypeArrays_NullInTypeArray_SetsNullableStripsFromRequired(t *testing.T) {
	in := `{"type":"object","properties":{"x":{"type":["string","null"]}},"required":["x"]}`
	out := flattenTypeArrays(in, true)
	if gjson.Get(out, "properties.x.type").String() != "string" {
		t.Fatalf("got %s", out)
	}
	if !gjson.Get(out, "properties.x.nullable").Bool() {
		t.Fatalf("expected nullable=true, got %s", out)
	}
	req := gjson.Get(out, "required")
	if req.Exists() {
		for _, r := range req.Array() {
			if r.String() == "x" {
				t.Fatalf("expected x stripped from required, got %s", out)
			}
		}
	}
}

func TestAdversarial_CyclicAndPercentRef(t *testing.T) {
	// Case 1: Root self-reference {"$ref": "#"}
	rootSelf := `{"type":"object","properties":{"children":{"type":"array","items":{"$ref":"#"}}}}`
	outRootSelf := CleanJSONSchemaForAntigravity("sess1", "TreeTool", rootSelf)
	t.Logf("Root self-ref output: %s", outRootSelf)

	// Case 2: Node recursive reference via $defs
	defsRecursive := `{
		"type":"object",
		"properties":{
			"node":{"$ref":"#/$defs/Node"}
		},
		"$defs":{
			"Node":{
				"type":"object",
				"properties":{
					"value":{"type":"string"},
					"children":{"type":"array","items":{"$ref":"#/$defs/Node"}}
				}
			}
		}
	}`
	outDefsRecursive := CleanJSONSchemaForAntigravity("sess1", "TreeTool2", defsRecursive)
	t.Logf("Defs recursive output: %s", outDefsRecursive)

	// Case 3: Percent-encoded ref "#/$defs/My%20Type"
	percentRef := `{
		"type":"object",
		"properties":{"field":{"$ref":"#/$defs/My%20Type"}},
		"$defs":{"My Type":{"type":"string","description":"My custom type"}}
	}`
	outPercent := CleanJSONSchemaForAntigravity("sess1", "PercentTool", percentRef)
	t.Logf("Percent-encoded ref output: %s", outPercent)

	// Case 4: Sibling properties alongside $ref
	siblingRef := `{
		"type":"object",
		"properties":{
			"field":{
				"$ref":"#/$defs/BaseStr",
				"nullable":true
			}
		},
		"$defs":{"BaseStr":{"type":"string"}}
	}`
	outSibling := CleanJSONSchemaForAntigravity("sess1", "SiblingTool", siblingRef)
	t.Logf("Sibling ref output: %s", outSibling)
}

func TestAdversarial_AnyOfThreeBranchesAndNested(t *testing.T) {
	// 3 branches: object, string, and null
	threeBranches := `{
		"type":"object",
		"properties":{
			"target":{
				"anyOf":[
					{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]},
					{"type":"string"},
					{"type":"null"}
				]
			}
		},
		"required":["target"]
	}`
	outThree := CleanJSONSchemaForAntigravity("sess1", "ThreeTool", threeBranches)
	t.Logf("Three branches output: %s", outThree)

	// Nested anyOf inside object properties
	nestedAnyOf := `{
		"type":"object",
		"properties":{
			"filter":{
				"anyOf":[
					{
						"type":"object",
						"properties":{
							"op":{
								"anyOf":[
									{"type":"string","enum":["eq","ne"]},
									{"type":"string","enum":["gt","lt"]}
								]
							}
						}
					},
					{"type":"string"}
				]
			}
		}
	}`
	outNested := CleanJSONSchemaForAntigravity("sess1", "NestedTool", nestedAnyOf)
	t.Logf("Nested anyOf output: %s", outNested)
}

