// Rule 5 — SchemaKeywordAllowlist. Ported verbatim from superrelay
// (v1)'s cmd/speccheck/rules.go (checkSchemaKeywordAllowlist): Gemini's
// OpenAPI-3.0 Protobuf schema validator rejects any key outside a known
// small subset. This is an INDEPENDENT re-check of internal/schema's own
// stripping list, from outside that code — it catches a future code
// change that silently stops stripping one of these.
package rules

import "fmt"

var geminiAllowedSchemaKeys = map[string]bool{
	"type": true, "description": true, "properties": true, "required": true,
	"items": true, "enum": true, "nullable": true, "format": true,
}

func checkSchemaKeywordAllowlist(rec CaptureRecord) []Violation {
	body := asMap(rec.snapshotBody("gemini_request"))
	if body == nil {
		return nil
	}
	var out []Violation
	for _, t := range asArray(body["tools"]) {
		tool := asMap(t)
		decls := asArray(tool["functionDeclarations"])
		for _, d := range decls {
			decl := asMap(d)
			name := asString(decl["name"])
			params := asMap(decl["parameters"])
			walkSchemaKeys(params, name, &out, rec)
		}
	}
	return out
}

func walkSchemaKeys(node map[string]interface{}, toolName string, out *[]Violation, rec CaptureRecord) {
	if node == nil {
		return
	}
	for k, v := range node {
		if !geminiAllowedSchemaKeys[k] {
			*out = append(*out, violation(rec, fmt.Sprintf("tool %q schema has unsupported key %q — internal/schema's stripping list may have regressed", toolName, k)))
		}
		switch k {
		case "properties":
			for _, prop := range asMap(v) {
				walkSchemaKeys(asMap(prop), toolName, out, rec)
			}
		case "items":
			walkSchemaKeys(asMap(v), toolName, out, rec)
		}
	}
}

var SchemaKeywordAllowlist = Rule{
	Name: "SchemaKeywordAllowlist",
	Check: func(records []CaptureRecord) []Violation {
		var out []Violation
		for _, rec := range records {
			out = append(out, checkSchemaKeywordAllowlist(rec)...)
		}
		return out
	},
}
