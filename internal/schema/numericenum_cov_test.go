package schema

import "testing"

func TestRecordNumericEnumFields_ArrayShapesInferredAndNested(t *testing.T) {
	for _, c := range []struct {
		name, schema string
		want         map[string]string
	}{
		{"items type inferred from enum", `{"properties":{"xs":{"type":"array","items":{"enum":[null,2]}}}}`,
			map[string]string{"xs": "array:integer"}},
		{"items x-orig-type wins", `{"properties":{"xs":{"type":"array","items":{"type":"string","x-orig-type":"number","enum":["1.5"]}}}}`,
			map[string]string{"xs": "array:number"}},
		{"array of boolean enum items without array type", `{"properties":{"xs":{"items":{"enum":[true,false]}}}}`,
			map[string]string{"xs": "array:boolean"}},
		{"2D inner type inferred", `{"properties":{"g":{"type":"array","items":{"type":"array","items":{"enum":[0.5]}}}}}`,
			map[string]string{"g": "array2D:number"}},
		{"2D inner x-orig-type", `{"properties":{"g":{"type":"array","items":{"type":"array","items":{"type":"string","x-orig-type":"integer","enum":["1"]}}}}}`,
			map[string]string{"g": "array2D:integer"}},
		{"2D string inner is not numeric", `{"properties":{"g":{"type":"array","items":{"type":"array","items":{"type":"string","enum":["a"]}}}}}`, nil},
		{"item property type inferred from enum", `{"properties":{"rows":{"type":"array","items":{"properties":{"p":{"enum":[1,2]}}}}}}`,
			map[string]string{"rows[].p": "arrayObject:integer"}},
		{"item property x-orig-type", `{"properties":{"rows":{"type":"array","items":{"properties":{"p":{"type":"string","x-orig-type":"boolean","enum":["true"]}}}}}}`,
			map[string]string{"rows[].p": "arrayObject:boolean"}},
		{"array inside array-item object", `{"properties":{"rows":{"type":"array","items":{"properties":{
			"tags":{"type":"array","items":{"type":"integer","enum":[1,2]}}}}}}}`,
			map[string]string{"rows[].tags": "array:integer"}},
		{"item property with string enum ignored", `{"properties":{"rows":{"type":"array","items":{"properties":{"s":{"type":"string","enum":["a"]}}}}}}`, nil},
		{"non-object item properties ignored", `{"properties":{"rows":{"type":"array","items":{"properties":["x"]}}}}`, nil},
		{"items property array2D inside item object", `{"properties":{"rows":{"type":"array","items":{"properties":{
			"m":{"type":"array","items":{"type":"array","items":{"type":"number","enum":[1.5]}}}}}}}}`,
			map[string]string{"rows[].m": "array2D:number"}},
	} {
		sess := enumSession(t)
		RecordNumericEnumFields(sess, "T", c.schema)
		got := recorded(t, sess, "T")
		if len(got) != len(c.want) {
			t.Errorf("%s: recorded %v, want %v", c.name, got, c.want)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s: field %q = %q, want %q (all: %v)", c.name, k, got[k], v, got)
			}
		}
	}
}
