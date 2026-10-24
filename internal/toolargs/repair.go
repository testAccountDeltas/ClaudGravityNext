// Package toolargs — repair.go holds unescaped control character repair
// and scalar argument unwrapping helpers, split from toolargs.go to stay under
// the 400-line ceiling.
package toolargs

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/tidwall/gjson"
)

// repairUnescapedControlChars walks raw byte-by-byte, tracking whether the
// scan is currently inside a JSON string literal (toggling on an
// unescaped '"', never on one preceded by an odd run of backslashes), and
// replaces a literal newline/CR/tab found INSIDE a string literal with its
// escaped form. Structural whitespace between tokens (outside any string)
// is left untouched. Returns raw unchanged if no string-literal control
// character was found, so the caller can cheaply detect "nothing to
// retry" without a second unmarshal attempt.
func repairUnescapedControlChars(raw string) string {
	changed := false
	trimmed := strings.TrimSpace(raw)
	// If JSON uses Python-style single quotes without any double quotes: convert to double quotes.
	if (strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")) && !strings.Contains(raw, `"`) && strings.Contains(raw, `'`) {
		raw = strings.ReplaceAll(raw, `'`, `"`)
		changed = true
	}

	var b strings.Builder
	inString := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !inString {
			if c == '"' {
				inString = true
			} else if c == ',' {
				// Peek ahead: skip trailing comma before closing } or ]
				peek := i + 1
				for peek < len(raw) && (raw[peek] == ' ' || raw[peek] == '\t' || raw[peek] == '\r' || raw[peek] == '\n') {
					peek++
				}
				if peek < len(raw) && (raw[peek] == '}' || raw[peek] == ']') {
					changed = true
					continue
				}
			}
			b.WriteByte(c)
			continue
		}
		if escaped {
			if c == '\n' {
				b.WriteString(`\n`)
				changed = true
			} else if c == '\r' {
				b.WriteString(`\r`)
				changed = true
			} else if c == '"' || c == '\\' || c == '/' || c == 'b' || c == 'f' || c == 'n' || c == 'r' || c == 't' {
				b.WriteByte(c)
			} else if c == 'u' {
				// Check if followed by 4 valid hex characters (valid JSON \uXXXX escape).
				// If not (e.g. Windows file path \users or \ui), double the backslash to escape it.
				isHex := true
				if i+4 >= len(raw) {
					isHex = false
				} else {
					for j := 1; j <= 4; j++ {
						hc := raw[i+j]
						if !((hc >= '0' && hc <= '9') || (hc >= 'a' && hc <= 'f') || (hc >= 'A' && hc <= 'F')) {
							isHex = false
							break
						}
					}
				}
				if isHex {
					b.WriteByte(c)
				} else {
					b.WriteByte('\\')
					b.WriteByte(c)
					changed = true
				}
			} else {
				// Invalid JSON escape (e.g. Windows file path \P or \C) — double the backslash
				b.WriteByte('\\')
				b.WriteByte(c)
				changed = true
			}
			escaped = false
			continue
		}
		switch c {
		case '\\':
			escaped = true
			b.WriteByte(c)
		case '"':
			inString = false
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
			changed = true
		case '\r':
			b.WriteString(`\r`)
			changed = true
		case '\t':
			b.WriteString(`\t`)
			changed = true
		default:
			if c < 0x20 {
				b.WriteString(fmt.Sprintf(`\u%04x`, c))
				changed = true
			} else {
				b.WriteByte(c)
			}
		}
	}
	if !changed {
		return raw
	}
	return b.String()
}

// UnwrapScalarArg detects if argsRaw contains the synthetic "__scalar_arg__"
// wrapper injected for bare-scalar root schemas (GAP-JS-09). If so, it unwraps
// and returns the value as a JSON object (mapping to {"value": <scalarVal>} if not a map);
// otherwise returns argsRaw.
//
// A string value is kept a string even when it merely looks like JSON ("123",
// "true", "[1]"): re-parsing it would change a literal into a number/array.
// Only when the optional scalarSchemaRaw (the tool's original bare-scalar
// root schema) declares object or array is a JSON-looking string decoded,
// since the model then serialized the structure into the string.
func UnwrapScalarArg(argsRaw string, scalarSchemaRaw ...string) string {
	var v interface{}
	if err := strictDecode(argsRaw, &v); err != nil {
		return argsRaw
	}
	m, ok := v.(map[string]interface{})
	if !ok {
		return argsRaw
	}
	scalarVal, exists := m["__scalar_arg__"]
	if !exists {
		return argsRaw
	}
	if innerMap, isMap := scalarVal.(map[string]interface{}); isMap {
		b, err := marshalNoEscape(innerMap)
		if err == nil {
			return string(b)
		}
		return argsRaw
	}
	if s, ok := scalarVal.(string); ok && len(scalarSchemaRaw) > 0 && schemaWantsStructure(scalarSchemaRaw[0]) {
		var inner interface{}
		if err := strictDecode(s, &inner); err == nil {
			if innerMap, ok := inner.(map[string]interface{}); ok {
				if b, err := marshalNoEscape(innerMap); err == nil {
					return string(b)
				}
			}
			if _, isArr := inner.([]interface{}); isArr {
				scalarVal = inner
			}
		}
	}
	b, err := marshalNoEscape(map[string]interface{}{"value": scalarVal})
	if err != nil {
		return argsRaw
	}
	return string(b)
}

// schemaWantsStructure reports whether schemaRaw's type is object or array.
func schemaWantsStructure(schemaRaw string) bool {
	t := gjson.Get(schemaRaw, "type")
	check := func(s string) bool { return s == "object" || s == "array" }
	if t.IsArray() {
		for _, e := range t.Array() {
			if check(e.String()) {
				return true
			}
		}
		return false
	}
	return check(t.String())
}

// strictDecode is json.Unmarshal that keeps numbers exact (json.Number)
// instead of rounding large integers through float64; trailing data after
// the value is an error, as with json.Unmarshal.
func strictDecode(raw string, v interface{}) error {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return fmt.Errorf("trailing data after JSON value")
	}
	return nil
}
