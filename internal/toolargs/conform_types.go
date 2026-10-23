package toolargs

// conform_types.go: per-type coercion (string/number/boolean/array/object).

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

func (c *conformer) typed(v interface{}, t string, s map[string]interface{}, path string, depth int) interface{} {
	switch t {
	case "string":
		switch x := v.(type) {
		case json.Number:
			v = x.String()
		case bool:
			v = strconv.FormatBool(x)
		case map[string]interface{}, []interface{}:
			if b, err := marshalNoEscape(x); err == nil {
				v = string(b)
			}
		}
	case "integer":
		if str, ok := v.(string); ok {
			v = parseNumericString(str, true, v)
		} else if n, ok := v.(json.Number); ok {
			if _, err := strconv.ParseInt(n.String(), 10, 64); err != nil {
				if f, err := strconv.ParseFloat(n.String(), 64); err == nil && f == math.Trunc(f) && math.Abs(f) < 1e15 {
					v = json.Number(strconv.FormatInt(int64(f), 10))
				}
			}
		}
	case "number":
		if str, ok := v.(string); ok {
			v = parseNumericString(str, false, v)
		}
	case "boolean":
		if str, ok := v.(string); ok {
			switch strings.ToLower(strings.TrimSpace(str)) {
			case "true", "1":
				v = true
			case "false", "0":
				v = false
			}
		}
	case "array":
		return c.array(v, s, path, depth)
	case "object":
		return c.object(v, s, path, depth)
	}
	return c.enumFix(v, s, path)
}

func parseNumericString(str string, integer bool, orig interface{}) interface{} {
	tr := strings.TrimSpace(str)
	if !numericStringRE.MatchString(tr) {
		return orig
	}
	if integer {
		if n, err := strconv.ParseInt(tr, 10, 64); err == nil {
			return json.Number(strconv.FormatInt(n, 10))
		}
		if f, err := strconv.ParseFloat(tr, 64); err == nil && f == math.Trunc(f) && math.Abs(f) < 1e15 {
			return json.Number(strconv.FormatInt(int64(f), 10))
		}
		return orig
	}
	if f, err := strconv.ParseFloat(tr, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
		return json.Number(strconv.FormatFloat(f, 'f', -1, 64))
	}
	return orig
}

func (c *conformer) array(v interface{}, s map[string]interface{}, path string, depth int) interface{} {
	if str, ok := v.(string); ok {
		tr := strings.TrimSpace(str)
		if strings.HasPrefix(tr, "[") {
			var parsed []interface{}
			if decodeUseNumber(tr, &parsed) == nil {
				v = parsed
			}
		}
	}
	arr, ok := v.([]interface{})
	if !ok {
		return v
	}
	switch items := s["items"].(type) {
	case map[string]interface{}:
		for i := range arr {
			arr[i] = c.itemValue(arr[i], items, fmt.Sprintf("%s[%d]", path, i), depth)
		}
	case []interface{}:
		for i := range arr {
			if i < len(items) {
				if im := asMap(items[i]); im != nil {
					arr[i] = c.itemValue(arr[i], im, fmt.Sprintf("%s[%d]", path, i), depth)
				}
			}
		}
	}
	return arr
}

// itemValue conforms one array element; a null element that the items
// schema does not allow is kept (dropping it would shift positions).
func (c *conformer) itemValue(v interface{}, items map[string]interface{}, path string, depth int) interface{} {
	return c.value(v, items, path, depth+1)
}

func (c *conformer) object(v interface{}, s map[string]interface{}, path string, depth int) interface{} {
	if str, ok := v.(string); ok {
		tr := strings.TrimSpace(str)
		if strings.HasPrefix(tr, "{") {
			var parsed map[string]interface{}
			if decodeUseNumber(tr, &parsed) == nil {
				v = parsed
			}
		}
	}
	m, ok := v.(map[string]interface{})
	if !ok {
		return v
	}
	props := asMap(s["properties"])
	addl := asMap(s["additionalProperties"])
	for k, val := range m {
		var ps map[string]interface{}
		if p := asMap(props[k]); p != nil {
			ps = p
		} else if addl != nil {
			ps = addl
		}
		sub := path + "." + k
		if path == "" {
			sub = k
		}
		if val == nil {
			if ps == nil || !c.allowsNull(ps) {
				delete(m, k)
			}
			continue
		}
		if ps != nil {
			m[k] = c.value(val, ps, sub, depth+1)
		}
	}
	if req, ok := s["required"].([]interface{}); ok {
		for _, r := range req {
			name, _ := r.(string)
			if _, present := m[name]; present || name == "" {
				continue
			}
			if name == "activeForm" {
				if content, ok := m["content"].(string); ok && content != "" {
					m["activeForm"] = content
					continue
				}
			}
			c.issues = append(c.issues, fmt.Sprintf("missing required argument %q at %s", name, pathOrRoot(path)))
		}
	}
	return m
}
