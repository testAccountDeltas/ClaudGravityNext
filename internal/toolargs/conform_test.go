package toolargs

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const readSchema = `{"type":"object","properties":{"file_path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"},"pages":{"type":"string"}},"required":["file_path"]}`

func TestConform_StringFieldStaysString(t *testing.T) {
	out, _ := ConformToSchema(`{"file_path":"a.pdf","pages":"3","offset":"15","limit":50}`, readSchema)
	if gjson.Get(out, "pages").Type != gjson.String || gjson.Get(out, "pages").String() != "3" {
		t.Fatalf("pages must stay string \"3\": %s", out)
	}
	if gjson.Get(out, "offset").Type != gjson.Number || gjson.Get(out, "offset").Int() != 15 {
		t.Fatalf("offset must coerce to 15: %s", out)
	}
}

func TestConform_NumberForStringFieldBecomesString(t *testing.T) {
	out, _ := ConformToSchema(`{"new_source":123,"args":{"a":1}}`,
		`{"type":"object","properties":{"new_source":{"type":"string"},"args":{"type":"string"}}}`)
	if gjson.Get(out, "new_source").String() != "123" || gjson.Get(out, "new_source").Type != gjson.String {
		t.Fatalf("got %s", out)
	}
	if gjson.Get(out, "args").Type != gjson.String {
		t.Fatalf("object for string field should be JSON-encoded: %s", out)
	}
}

func TestConform_NumbersBooleansFromStrings(t *testing.T) {
	sch := `{"type":"object","properties":{"timeout":{"type":"integer"},"score":{"type":"number"},"replace_all":{"type":"boolean"},"id":{"type":"string"}}}`
	out, _ := ConformToSchema(`{"timeout":"30","score":"9.5","replace_all":"true","id":"0123"}`, sch)
	if gjson.Get(out, "timeout").Int() != 30 || gjson.Get(out, "timeout").Type != gjson.Number {
		t.Fatalf("%s", out)
	}
	if gjson.Get(out, "score").Float() != 9.5 || gjson.Get(out, "replace_all").Type != gjson.True {
		t.Fatalf("%s", out)
	}
	if gjson.Get(out, "id").String() != "0123" {
		t.Fatalf("string id must be untouched: %s", out)
	}
}

func TestConform_NullsKeptOnlyWhereSchemaAllowsNull(t *testing.T) {
	sch := `{"type":"object","properties":{"a":{"type":["string","null"]},"b":{"type":"string"},"c":{"anyOf":[{"type":"integer"},{"type":"null"}]},"n":{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"string","nullable":true}}}}}`
	out, _ := ConformToSchema(`{"a":null,"b":null,"c":null,"d":null,"n":{"x":null,"y":null}}`, sch)
	if !gjson.Get(out, "a").Exists() || !gjson.Get(out, "c").Exists() || !gjson.Get(out, "n.y").Exists() {
		t.Fatalf("meaningful nulls were removed: %s", out)
	}
	if gjson.Get(out, "b").Exists() || gjson.Get(out, "d").Exists() || gjson.Get(out, "n.x").Exists() {
		t.Fatalf("non-nullable nulls must be dropped: %s", out)
	}
}

func TestConform_UnionStringToArrayAndObject(t *testing.T) {
	sch := `{"type":"object","properties":{"files":{"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"object","properties":{"k":{"type":"integer"}}}]},"q":{"anyOf":[{"type":"string"},{"type":"array","items":{"type":"string"}}]}}}`
	out, _ := ConformToSchema(`{"files":"[\"a\",\"b\"]","q":"plain"}`, sch)
	if !gjson.Get(out, "files").IsArray() || gjson.Get(out, "files.1").String() != "b" {
		t.Fatalf("JSON string must parse into array branch: %s", out)
	}
	if gjson.Get(out, "q").Type != gjson.String {
		t.Fatalf("string branch must be kept: %s", out)
	}
	out, _ = ConformToSchema(`{"files":"{\"k\":\"7\"}"}`, sch)
	if gjson.Get(out, "files.k").Type != gjson.Number {
		t.Fatalf("string must parse into object branch and coerce inside: %s", out)
	}
}

func TestConform_RefsAndNestedItems(t *testing.T) {
	sch := `{"type":"object","properties":{"todos":{"type":"array","items":{"$ref":"#/$defs/Todo"}}},"$defs":{"Todo":{"type":"object","properties":{"content":{"type":"string"},"status":{"type":"string","enum":["pending","in_progress","completed"]},"activeForm":{"type":"string"}},"required":["content","status","activeForm"]}}}`
	out, issues := ConformToSchema(`{"todos":[{"content":"Fix bug","status":"in-progress"},{"content":"Ship","status":"Completed","activeForm":"Shipping"}]}`, sch)
	if gjson.Get(out, "todos.0.status").String() != "in_progress" || gjson.Get(out, "todos.1.status").String() != "completed" {
		t.Fatalf("enum not repaired: %s", out)
	}
	if gjson.Get(out, "todos.0.activeForm").String() != "Fix bug" {
		t.Fatalf("activeForm not derived: %s", out)
	}
	if len(issues) != 0 {
		t.Fatalf("unexpected issues %v", issues)
	}
}

func TestConform_MissingRequiredAndBadEnumReported(t *testing.T) {
	sch := `{"type":"object","properties":{"mode":{"type":"string","enum":["a","b"]},"path":{"type":"string"}},"required":["path"]}`
	out, issues := ConformToSchema(`{"mode":"zzz"}`, sch)
	if gjson.Get(out, "path").Exists() {
		t.Fatalf("must not invent a missing required arg: %s", out)
	}
	joined := strings.Join(issues, "|")
	if !strings.Contains(joined, "missing required") || !strings.Contains(joined, "not in enum") {
		t.Fatalf("issues = %v", issues)
	}
}

func TestConform_DiscriminatedUnionPicksMatchingBranch(t *testing.T) {
	sch := `{"anyOf":[{"type":"object","properties":{"action":{"const":1},"a":{"type":"integer"}}},{"type":"object","properties":{"action":{"const":2},"b":{"type":"integer"}}}]}`
	out, issues := ConformToSchema(`{"action":"2","b":"5"}`, sch)
	if gjson.Get(out, "action").Int() != 2 || gjson.Get(out, "b").Int() != 5 || len(issues) != 0 {
		t.Fatalf("%s %v", out, issues)
	}
}

func TestPruneTopLevelNulls_KeepsNested(t *testing.T) {
	out := PruneTopLevelNulls(`{"a":null,"b":{"c":null}}`)
	if gjson.Get(out, "a").Exists() || !gjson.Get(out, "b.c").Exists() {
		t.Fatalf("%s", out)
	}
}

func TestNormalize_TextEditorNewNameBareString(t *testing.T) {
	if got := NormalizeToArgsObject("str_replace_based_edit_tool", `"/a/b.go"`); gjson.Get(got, "path").String() != "/a/b.go" {
		t.Fatalf("%s", got)
	}
}
