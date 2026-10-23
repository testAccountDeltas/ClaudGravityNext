package schema

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAddMissingArrayItems_BareArray_ForTool_GetsEmptyItems(t *testing.T) {
	out := addMissingArrayItems(`{"type":"array"}`, true)
	if gjson.Get(out, "items").Raw != "{}" {
		t.Fatalf("expected items={}, got %s", out)
	}
}

func TestCleanJSONSchema_NestedAllOfInAnyOf_MergedAndCleaned(t *testing.T) {
	// GAP-V2-05: allOf nested inside anyOf must be merged when anyOf is flattened,
	// and no leftover "allOf" keyword should survive to reach Gemini.
	in := `{"type":"object","properties":{"item":{"anyOf":[{"allOf":[{"type":"object","properties":{"a":{"type":"string"}}},{"properties":{"b":{"type":"number"}}}]}]}}}`
	out := CleanJSONSchemaForGemini(in)
	if gjson.Get(out, "properties.item.allOf").Exists() {
		t.Fatalf("allOf must not survive in output schema, got %s", out)
	}
	if !gjson.Get(out, "properties.item.properties.a").Exists() || !gjson.Get(out, "properties.item.properties.b").Exists() {
		t.Fatalf("expected both properties a and b merged, got %s", out)
	}
}

// GAP-S3: the public Gemini API's own grammar compiler rejects a bare {}
// items schema outright — the Structured Outputs path (forTool==false)
// must get a non-empty default instead.
func TestAddMissingArrayItems_BareArray_ForResponseSchema_GetsTypedItems(t *testing.T) {
	out := addMissingArrayItems(`{"type":"array"}`, false)
	if gjson.Get(out, "items.type").String() != "string" {
		t.Fatalf("expected items.type=string, got %s", out)
	}
}

func TestAddMissingArrayItems_SkipsRequestContentsPath(t *testing.T) {
	// A function-call replay under "contents." can legitimately hold
	// "type":"array" as literal DATA, not schema — must not be touched.
	raw := `{"contents":{"parts":[{"functionCall":{"args":{"type":"array"}}}]}}`
	// findPaths walks the whole doc; simulate the guarded path directly.
	out := addMissingArrayItems(`{"contents":{"type":"array"}}`, true)
	if gjson.Get(out, "contents.items").Exists() {
		t.Fatalf("must not add items under a contents.* path, got %s", out)
	}
	_ = raw
}

func TestBareScalarRootSchema_WrappedInObject(t *testing.T) {
	// GAP-JS-09: A bare scalar string schema is wrapped into an object for Gemini
	scalarSchema := `{"type":"string","description":"SQL query"}`
	out := CleanJSONSchemaForAntigravity("sess1", "sql_query", scalarSchema)
	if gjson.Get(out, "type").String() != "object" {
		t.Fatalf("expected root type object, got %s", out)
	}
	if gjson.Get(out, "properties.__scalar_arg__.type").String() != "string" {
		t.Fatalf("expected __scalar_arg__ with type string, got %s", out)
	}
	if gjson.Get(out, "required.0").String() != "__scalar_arg__" {
		t.Fatalf("expected required=[__scalar_arg__], got %s", out)
	}
}

func TestBareSchemaEmptyObject_GetsReasonPlaceholder(t *testing.T) {
	// GAP-CGN-05: Bare "{}" schema gets object type and reason placeholder
	out := CleanJSONSchemaForAntigravity("sess1", "empty_tool", `{}`)
	if gjson.Get(out, "type").String() != "object" {
		t.Fatalf("expected root type object, got %s", out)
	}
	if !gjson.Get(out, "properties.reason").Exists() {
		t.Fatalf("expected reason placeholder in empty schema, got %s", out)
	}
	desc := gjson.Get(out, "properties.reason.description").String()
	if desc != "Placeholder" && desc != placeholderReasonDescription {
		t.Fatalf("expected reason description, got %s", desc)
	}
	if !HasTopLevelPlaceholderReason(out) {
		t.Fatalf("expected HasTopLevelPlaceholderReason to return true for empty schema placeholder")
	}

	// Test whitespace-only string
	outSpaces := CleanJSONSchemaForAntigravity("sess1", "empty_tool2", `   `)
	descSpaces := gjson.Get(outSpaces, "properties.reason.description").String()
	if descSpaces != "Placeholder" && descSpaces != placeholderReasonDescription {
		t.Fatalf("expected reason description for whitespace schema, got %s", descSpaces)
	}
}

func TestBareScalarRootSchema_AllTypes(t *testing.T) {
	// GAP-JS-09: Integer, boolean, and array bare root schemas
	intSchema := `{"type":"integer"}`
	outInt := CleanJSONSchemaForAntigravity("sess1", "int_tool", intSchema)
	if gjson.Get(outInt, "type").String() != "object" || gjson.Get(outInt, "properties.__scalar_arg__.type").String() != "integer" {
		t.Fatalf("expected wrapped integer schema, got %s", outInt)
	}
	if gjson.Get(outInt, "required.0").String() != "__scalar_arg__" {
		t.Fatalf("expected required=[__scalar_arg__], got %s", outInt)
	}

	boolSchema := `{"type":"boolean"}`
	outBool := CleanJSONSchemaForAntigravity("sess1", "bool_tool", boolSchema)
	if gjson.Get(outBool, "type").String() != "object" || gjson.Get(outBool, "properties.__scalar_arg__.type").String() != "boolean" {
		t.Fatalf("expected wrapped boolean schema, got %s", outBool)
	}

	arrSchema := `{"type":"array","items":{"type":"string"}}`
	outArr := CleanJSONSchemaForAntigravity("sess1", "arr_tool", arrSchema)
	if gjson.Get(outArr, "type").String() != "object" || gjson.Get(outArr, "properties.__scalar_arg__.type").String() != "array" {
		t.Fatalf("expected wrapped array schema, got %s", outArr)
	}
}

func TestNumericEnumInDiscriminatedUnion_RecordedAndCoerced(t *testing.T) {
	// GAP-CGN-01: Discriminated union with numeric consts preserves integer enum and coercion
	unionSchema := `{
		"anyOf": [
			{
				"type": "object",
				"properties": {
					"action": {"const": 1, "description": "start"},
					"start_arg": {"type": "string"}
				},
				"required": ["action"]
			},
			{
				"type": "object",
				"properties": {
					"action": {"const": 2, "description": "stop"},
					"stop_arg": {"type": "string"}
				},
				"required": ["action"]
			}
		]
	}`

	cleaned := CleanJSONSchemaForAntigravity("sess_num", "union_tool", unionSchema)
	// Gemini schema gets string enum
	if gjson.Get(cleaned, "properties.action.type").String() != "string" {
		t.Fatalf("expected Gemini schema to have action type string, got %s", cleaned)
	}

	// Model returns string argument "1"
	callArgs := `{"action":"1","start_arg":"foo"}`
	coerced := CoerceNumericEnumArgs("sess_num", "union_tool", callArgs)
	if gjson.Get(coerced, "action").Type != gjson.Number || gjson.Get(coerced, "action").Int() != 1 {
		t.Fatalf("expected action coerced back to integer 1, got %s", coerced)
	}
}

func TestBooleanEnumInDiscriminatedUnion_RecordedAndCoerced(t *testing.T) {
	// GAP-CGN-01: Discriminated union with boolean consts preserves boolean enum and coercion
	unionSchema := `{
		"anyOf": [
			{
				"type": "object",
				"properties": {
					"enabled": {"const": true},
					"on_mode": {"type": "string"}
				},
				"required": ["enabled"]
			},
			{
				"type": "object",
				"properties": {
					"enabled": {"const": false},
					"off_reason": {"type": "string"}
				},
				"required": ["enabled"]
			}
		]
	}`

	cleaned := CleanJSONSchemaForAntigravity("sess_bool", "bool_union_tool", unionSchema)
	if gjson.Get(cleaned, "properties.enabled.type").String() != "string" {
		t.Fatalf("expected Gemini schema to have enabled type string, got %s", cleaned)
	}

	callArgs := `{"enabled":"true","on_mode":"fast"}`
	coerced := CoerceNumericEnumArgs("sess_bool", "bool_union_tool", callArgs)
	if gjson.Get(coerced, "enabled").Type != gjson.True {
		t.Fatalf("expected enabled coerced back to boolean true, got %s", coerced)
	}
}

func TestFloatEnumInDiscriminatedUnion_RecordedAndCoerced(t *testing.T) {
	// GAP-CGN-01: Discriminated union with float consts preserves number enum and coercion
	unionSchema := `{
		"anyOf": [
			{
				"type": "object",
				"properties": {
					"version": {"const": 1.5},
					"v1_flag": {"type": "boolean"}
				},
				"required": ["version"]
			},
			{
				"type": "object",
				"properties": {
					"version": {"const": 2.5},
					"v2_flag": {"type": "boolean"}
				},
				"required": ["version"]
			}
		]
	}`

	cleaned := CleanJSONSchemaForAntigravity("sess_float", "float_union_tool", unionSchema)
	if gjson.Get(cleaned, "properties.version.type").String() != "string" {
		t.Fatalf("expected Gemini schema to have version type string, got %s", cleaned)
	}

	callArgs := `{"version":"2.5","v2_flag":true}`
	coerced := CoerceNumericEnumArgs("sess_float", "float_union_tool", callArgs)
	if gjson.Get(coerced, "version").Type != gjson.Number || gjson.Get(coerced, "version").Float() != 2.5 {
		t.Fatalf("expected version coerced back to float 2.5, got %s", coerced)
	}
}

func TestParameterNamedProperties_UnsupportedKeywordsStripped(t *testing.T) {
	// Finding 1: Parameter named literally "properties" should have its unsupported
	// keywords (like additionalProperties, minLength) properly stripped.
	schema := `{
		"type": "object",
		"properties": {
			"properties": {
				"type": "object",
				"additionalProperties": false,
				"minLength": 1,
				"properties": {
					"sub_prop": {"type": "string"}
				}
			}
		}
	}`
	cleaned := CleanJSONSchemaForAntigravity("sess_props", "props_tool", schema)
	if gjson.Get(cleaned, "properties.properties.additionalProperties").Exists() {
		t.Fatalf("expected additionalProperties stripped inside parameter named 'properties', got %s", cleaned)
	}
	if gjson.Get(cleaned, "properties.properties.minLength").Exists() {
		t.Fatalf("expected minLength stripped inside parameter named 'properties', got %s", cleaned)
	}
	if gjson.Get(cleaned, "properties.properties.type").String() != "object" {
		t.Fatalf("expected properties parameter to be preserved as object, got %s", cleaned)
	}
	if gjson.Get(cleaned, "properties.properties.properties.sub_prop.type").String() != "string" {
		t.Fatalf("expected sub_prop to be preserved, got %s", cleaned)
	}
}

func TestDeeplyNestedPolymorphicSchema(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"query": {
				"anyOf": [
					{
						"type": "object",
						"properties": {
							"rules": {
								"type": "array",
								"items": {
									"oneOf": [
										{
											"type": "object",
											"properties": {
												"filter": {
													"anyOf": [
														{"type": "string", "enum": ["a", "b"]},
														{"type": "string", "enum": ["c"]}
													]
												}
											},
											"required": ["filter"]
										},
										{
											"type": "object",
											"properties": {
												"num": {"type": "integer"}
											}
										}
									]
								}
							}
						}
					}
				]
			}
		}
	}`
	cleaned := CleanJSONSchemaForAntigravity("sess1", "nested_tool", schema)
	if gjson.Get(cleaned, "properties.query.anyOf").Exists() {
		t.Fatalf("anyOf leaked at query: %s", cleaned)
	}
	if gjson.Get(cleaned, "properties.query.properties.rules.items.oneOf").Exists() {
		t.Fatalf("oneOf leaked at rules.items: %s", cleaned)
	}
	if gjson.Get(cleaned, "properties.query.properties.rules.items.properties.filter.anyOf").Exists() {
		t.Fatalf("anyOf leaked at filter: %s", cleaned)
	}
	if gjson.Get(cleaned, "properties.query.type").String() != "object" {
		t.Fatalf("expected query to be object, got %s", cleaned)
	}
	if gjson.Get(cleaned, "properties.query.properties.rules.type").String() != "array" {
		t.Fatalf("expected rules to be array, got %s", cleaned)
	}
}

func TestPropertyNamedWithDotProperties_ArrayItemsMissing(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"app.properties": {
				"type": "array"
			}
		}
	}`
	cleaned := CleanJSONSchemaForAntigravity("sess_dot_arr", "dot_arr_tool", schema)
	if !gjson.Get(cleaned, "properties.app\\.properties.items").Exists() {
		t.Errorf("FAIL: items missing for array in app.properties: %s", cleaned)
	}
}

func TestPropertyNamedWithDotProperties_UnsupportedKeywordsStripped(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"app.properties": {
				"type": "string",
				"minLength": 5,
				"additionalProperties": false
			}
		}
	}`
	cleaned := CleanJSONSchemaForAntigravity("sess_dot", "dot_tool", schema)
	if gjson.Get(cleaned, "properties.app\\.properties.minLength").Exists() {
		t.Errorf("FAIL: minLength leaked in app.properties: %s", cleaned)
	}
	if gjson.Get(cleaned, "properties.app\\.properties.additionalProperties").Exists() {
		t.Errorf("FAIL: additionalProperties leaked in app.properties: %s", cleaned)
	}
}

func TestUppercaseObjectType_NotWrappedAsScalar(t *testing.T) {
	schema := `{"type":"OBJECT","properties":{"arg":{"type":"STRING"}}}`
	cleaned := CleanJSONSchemaForAntigravity("sess_upper", "upper_tool", schema)
	if gjson.Get(cleaned, "properties.__scalar_arg__").Exists() {
		t.Fatalf("expected uppercase OBJECT not to be wrapped as scalar, got %s", cleaned)
	}
	if !gjson.Get(cleaned, "properties.arg").Exists() {
		t.Fatalf("expected property arg to exist, got %s", cleaned)
	}
}

func TestRecursiveSchema_CircularReference(t *testing.T) {
	schema := `{
			"type": "object",
			"properties": {
				"node": {"$ref": "#/$defs/Node"}
			},
			"$defs": {
				"Node": {
					"type": "object",
					"properties": {
						"val": {"type": "string"},
						"next": {"$ref": "#/$defs/Node"}
					}
				}
			}
		}`
	cleaned := CleanJSONSchemaForAntigravity("sess_rec", "rec_tool", schema)
	t.Logf("Cleaned recursive schema: %s", cleaned)
	if gjson.Get(cleaned, "$defs").Exists() {
		t.Fatalf("$defs should be stripped, got %s", cleaned)
	}
	if gjson.Get(cleaned, "properties.node.properties.val.type").String() != "string" {
		t.Fatalf("expected node.val=string, got %s", cleaned)
	}
	desc := gjson.Get(cleaned, "properties.node.properties.next.properties.next.description").String()
	if desc != "Recursive reference: See Node" {
		t.Fatalf("expected recursive description, got %s", desc)
	}
}

func TestCleanJSONSchemaForGemini_RootEnumWithoutType_InfersStringType(t *testing.T) {
	in := `{"enum": ["active", "inactive"]}`
	cleaned := CleanJSONSchemaForGemini(in)
	if gjson.Get(cleaned, "type").String() == "object" {
		t.Fatalf("root enum must not be typed as object in responseSchema, got %s", cleaned)
	}
	if gjson.Get(cleaned, "type").String() != "string" {
		t.Fatalf("expected inferred type string for root enum, got %s", cleaned)
	}
}

func TestMergeAllOfPropertiesInto_ItemsRequired_IntersectWhenNotUnion(t *testing.T) {
	// Branch A requires ["id", "extra"] in items, Branch B requires ["id", "other"] in items
	dest := `{"type":"object","properties":{"items_list":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"extra":{"type":"string"}},"required":["id","extra"]}}}}`
	branchBProps := gjson.Parse(`{"items_list":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"other":{"type":"string"}},"required":["id","other"]}}}`)
	merged := mergeAllOfPropertiesInto(dest, "properties", branchBProps, false)
	req := gjson.Get(merged, "properties.items_list.items.required")
	if !req.IsArray() {
		t.Fatalf("expected required array, got %s", merged)
	}
	var reqFields []string
	for _, v := range req.Array() {
		reqFields = append(reqFields, v.String())
	}
	if len(reqFields) != 1 || reqFields[0] != "id" {
		t.Fatalf("expected intersection of items.required to be [\"id\"], got %v (full: %s)", reqFields, merged)
	}
}

func TestMergeObjectBranches_ArrayItemsRequired_Intersects(t *testing.T) {
	branches := []gjson.Result{
		gjson.Parse(`{"type":"object","properties":{"files":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"format":{"type":"string"}},"required":["path","format"]}}}}`),
		gjson.Parse(`{"type":"object","properties":{"files":{"type":"array","items":{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}}}}`),
	}
	out := mergeObjectBranches(branches)
	req := gjson.Get(out, "properties.files.items.required")
	if !req.IsArray() {
		t.Fatalf("expected required array, got %s", out)
	}
	var reqFields []string
	for _, v := range req.Array() {
		reqFields = append(reqFields, v.String())
	}
	if len(reqFields) != 1 || reqFields[0] != "path" {
		t.Fatalf("expected intersected items.required to be [\"path\"], got %v (full: %s)", reqFields, out)
	}
}

func TestCleanupRequiredFields_StripsNullableFieldsForTool(t *testing.T) {
	// A tool schema where an enum contains null, and another property is declared with nullable: true
	schema := `{
		"type": "object",
		"properties": {
			"mode": {"enum": ["fast", "slow", null]},
			"opt_num": {"type": "number", "nullable": true},
			"req_str": {"type": "string"}
		},
		"required": ["mode", "opt_num", "req_str"]
	}`
	// For Antigravity (tools): nullable fields must be stripped from required
	cleanedTool := CleanJSONSchemaForAntigravity("test-session", "my_tool", schema)
	req := gjson.Get(cleanedTool, "required")
	if !req.IsArray() {
		t.Fatalf("expected required array, got %s", cleanedTool)
	}
	var reqFields []string
	for _, v := range req.Array() {
		reqFields = append(reqFields, v.String())
	}
	if len(reqFields) != 1 || reqFields[0] != "req_str" {
		t.Fatalf("expected only non-nullable req_str in required, got %v (full: %s)", reqFields, cleanedTool)
	}

	// For Gemini (Structured Outputs): nullable fields must be preserved in required
	cleanedGemini := CleanJSONSchemaForGemini(schema)
	reqGemini := gjson.Get(cleanedGemini, "required")
	if !reqGemini.IsArray() || len(reqGemini.Array()) != 3 {
		t.Fatalf("expected all 3 fields preserved in required for Structured Outputs, got %s", cleanedGemini)
	}
}

func TestStripKeysNotInAllowlist_EnforcesOpenAPI30Whitelist(t *testing.T) {
	schema := `{
			"type": "object",
			"title": "MyCustomTool",
			"markdownDescription": "A tool with exotic keywords",
			"customRootProp": true,
			"properties": {
				"param1": {
					"type": "string",
					"markdownDescription": "Markdown documentation",
					"author": "Alice",
					"customBadge": "v1.2",
					"example": "test"
				},
				"items_list": {
					"type": "array",
					"items": {
						"type": "object",
						"extraItemMeta": 42,
						"properties": {
							"id": {"type": "string", "badKeyword": "foo"}
						}
					}
				}
			}
		}`

	cleaned := CleanJSONSchemaForAntigravity("session", "MyCustomTool", schema)

	// 1. Root level checks: customRootProp, title, markdownDescription must be stripped
	if gjson.Get(cleaned, "customRootProp").Exists() {
		t.Fatalf("expected customRootProp stripped, got %s", cleaned)
	}
	if gjson.Get(cleaned, "markdownDescription").Exists() {
		t.Fatalf("expected root markdownDescription stripped, got %s", cleaned)
	}
	if !strings.Contains(gjson.Get(cleaned, "description").String(), "A tool with exotic keywords") {
		t.Fatalf("expected root description to contain markdownDescription, got %s", cleaned)
	}

	// 2. param1 checks: author, customBadge, example stripped, markdownDescription rescued
	if gjson.Get(cleaned, "properties.param1.author").Exists() {
		t.Fatalf("expected author stripped, got %s", cleaned)
	}
	if gjson.Get(cleaned, "properties.param1.customBadge").Exists() {
		t.Fatalf("expected customBadge stripped, got %s", cleaned)
	}
	if !strings.Contains(gjson.Get(cleaned, "properties.param1.description").String(), "Markdown documentation") {
		t.Fatalf("expected param1 description to contain markdownDescription, got %s", cleaned)
	}

	// 3. Array items checks: extraItemMeta, badKeyword stripped
	if gjson.Get(cleaned, "properties.items_list.items.extraItemMeta").Exists() {
		t.Fatalf("expected extraItemMeta stripped, got %s", cleaned)
	}
	if gjson.Get(cleaned, "properties.items_list.items.properties.id.badKeyword").Exists() {
		t.Fatalf("expected badKeyword stripped, got %s", cleaned)
	}
}
