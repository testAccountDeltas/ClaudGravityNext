// Package toolargs — conform.go coerces a generated tool call's arguments
// to the tool's DECLARED JSON schema. This replaces the earlier
// field-NAME based coercion (a "pages":"3" became the integer 3 because
// the name looked numeric, a string-typed Skill.args became a number,
// every null was deleted): the declared schema is the only authority on
// what a field's type is, so a string field stays a string and an
// integer field accepts "3".
package toolargs

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

var numericStringRE = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

type conformer struct {
	root   map[string]interface{}
	issues []string
}

// ConformToSchema coerces argsRaw (a JSON object) to schemaRaw (the tool's
// ORIGINAL input_schema, $refs and all). It returns the possibly-rewritten
// args plus human-readable issues that could not be repaired (missing
// required argument, value outside an enum) for the caller to log — it
// never invents a missing value, except TodoWrite-style activeForm which
// is derivable from a sibling "content". Unparseable input is returned
// unchanged.
func ConformToSchema(argsRaw, schemaRaw string) (string, []string) {
	var args interface{}
	if err := decodeUseNumber(argsRaw, &args); err != nil {
		return argsRaw, nil
	}
	argsObj, ok := args.(map[string]interface{})
	if !ok {
		return argsRaw, nil
	}
	var sch map[string]interface{}
	if err := decodeUseNumber(schemaRaw, &sch); err != nil || sch == nil {
		return argsRaw, nil
	}
	c := &conformer{root: sch}
	rs := c.deref(sch)
	if t := schemaTypes(rs); len(t) > 0 && t[0] != "object" {
		return argsRaw, nil // scalar/array root is wrapped in __scalar_arg__ elsewhere
	}
	out := c.value(argsObj, sch, "", 0)
	b, err := marshalNoEscape(out)
	if err != nil {
		return argsRaw, c.issues
	}
	if jsonEqual(b, []byte(argsRaw)) {
		return argsRaw, c.issues
	}
	return string(b), c.issues
}

// PruneTopLevelNulls is the schema-less fallback: Gemini fills unset
// optional parameters with null, which Claude Code's validators reject.
// Only top-level keys are touched — without a schema a nested null may be
// meaningful.
func PruneTopLevelNulls(argsRaw string) string {
	var m map[string]interface{}
	if err := decodeUseNumber(argsRaw, &m); err != nil || m == nil {
		return argsRaw
	}
	changed := false
	for k, v := range m {
		if v == nil {
			delete(m, k)
			changed = true
		}
	}
	if !changed {
		return argsRaw
	}
	b, err := marshalNoEscape(m)
	if err != nil {
		return argsRaw
	}
	return string(b)
}

func decodeUseNumber(raw string, v interface{}) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	// Decode stops after the first value; trailing bytes ("{}0") make the
	// document invalid, and an invalid document must be left untouched.
	if _, err := dec.Token(); err != io.EOF {
		return errTrailingData
	}
	return nil
}

var errTrailingData = errors.New("trailing data after JSON value")

func marshalNoEscape(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func jsonEqual(a, b []byte) bool {
	var x, y interface{}
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ab, _ := json.Marshal(x)
	bb, _ := json.Marshal(y)
	return bytes.Equal(ab, bb)
}

// deref follows local "$ref" pointers (#/$defs/X, #/definitions/X).
func (c *conformer) deref(s map[string]interface{}) map[string]interface{} {
	for i := 0; i < 8; i++ {
		ref, ok := s["$ref"].(string)
		if !ok || !strings.HasPrefix(ref, "#/") {
			return s
		}
		var cur interface{} = c.root
		for _, part := range strings.Split(ref[2:], "/") {
			part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
			m, ok := cur.(map[string]interface{})
			if !ok {
				return s
			}
			cur = m[part]
		}
		next, ok := cur.(map[string]interface{})
		if !ok {
			return s
		}
		s = next
	}
	return s
}

func asMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

// schemaTypes returns the declared types of s excluding "null".
func schemaTypes(s map[string]interface{}) []string {
	var out []string
	switch t := s["type"].(type) {
	case string:
		if t != "null" {
			out = append(out, t)
		}
	case []interface{}:
		for _, e := range t {
			if es, ok := e.(string); ok && es != "null" {
				out = append(out, es)
			}
		}
	}
	if len(out) == 0 && s["type"] == nil {
		switch {
		case s["properties"] != nil:
			out = append(out, "object")
		case s["items"] != nil:
			out = append(out, "array")
		}
	}
	return out
}

func (c *conformer) allowsNull(s map[string]interface{}) bool {
	if s == nil {
		return false
	}
	s = c.deref(s)
	if b, _ := s["nullable"].(bool); b {
		return true
	}
	switch t := s["type"].(type) {
	case string:
		if t == "null" {
			return true
		}
	case []interface{}:
		for _, e := range t {
			if e == "null" {
				return true
			}
		}
	}
	if en, ok := s["enum"].([]interface{}); ok {
		for _, e := range en {
			if e == nil {
				return true
			}
		}
	}
	if k, ok := s["const"]; ok && k == nil {
		return true
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if br, ok := s[key].([]interface{}); ok {
			for _, b := range br {
				if c.allowsNull(asMap(b)) {
					return true
				}
			}
		}
	}
	return false
}

func (c *conformer) value(v interface{}, s map[string]interface{}, path string, depth int) interface{} {
	if s == nil || depth > 24 || v == nil {
		return v
	}
	s = c.deref(s)
	if all, ok := s["allOf"].([]interface{}); ok {
		for _, b := range all {
			if bm := asMap(b); bm != nil {
				v = c.value(v, bm, path, depth+1)
			}
		}
	}
	for _, key := range []string{"anyOf", "oneOf"} {
		if br, ok := s[key].([]interface{}); ok && len(br) > 0 {
			return c.union(v, br, path, depth)
		}
	}
	types := schemaTypes(s)
	switch len(types) {
	case 0:
		return c.enumFix(v, s, path)
	case 1:
		return c.typed(v, types[0], s, path, depth)
	}
	branches := make([]interface{}, 0, len(types))
	for _, t := range types {
		cp := make(map[string]interface{}, len(s))
		for k, val := range s {
			cp[k] = val
		}
		cp["type"] = t
		branches = append(branches, cp)
	}
	return c.union(v, branches, path, depth)
}
