package toolargs

// conform_union.go: union branch selection, enum repair.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// union picks the branch the value already satisfies (fewest issues
// wins), else the first branch the value can be coerced to — which is
// what lets a JSON-encoded string become the array/object branch.
func (c *conformer) union(v interface{}, branches []interface{}, path string, depth int) interface{} {
	var cands []map[string]interface{}
	for _, b := range branches {
		bm := asMap(b)
		if bm == nil {
			continue
		}
		bm = c.deref(bm)
		if t, _ := bm["type"].(string); t == "null" {
			continue
		}
		cands = append(cands, bm)
	}
	base := len(c.issues)
	// The cleaned schema told the model to pass non-string forms of a
	// mixed union as a JSON-encoded string: when the string parses as the
	// array/object another branch declares, that branch wins.
	if str, ok := v.(string); ok {
		tr := strings.TrimSpace(str)
		want := ""
		switch {
		case strings.HasPrefix(tr, "["):
			want = "array"
		case strings.HasPrefix(tr, "{"):
			want = "object"
		}
		if want != "" && json.Valid([]byte(tr)) {
			for _, bm := range cands {
				if ts := schemaTypes(bm); len(ts) == 1 && ts[0] == want {
					if out := c.value(v, bm, path, depth+1); c.matches(out, bm) {
						return out
					}
				}
			}
		}
	}
	best, bestIssues := interface{}(nil), -1
	var bestList []string
	for _, bm := range cands {
		if !c.matches(v, bm) {
			continue
		}
		c.issues = c.issues[:base]
		out := c.value(v, bm, path, depth+1)
		n := len(c.issues) - base
		if bestIssues < 0 || n < bestIssues {
			best, bestIssues = out, n
			bestList = append([]string(nil), c.issues[base:]...)
		}
		if n == 0 {
			break
		}
	}
	if bestIssues >= 0 {
		c.issues = append(c.issues[:base], bestList...)
		return best
	}
	for _, bm := range cands {
		c.issues = c.issues[:base]
		out := c.value(v, bm, path, depth+1)
		if len(c.issues) == base && c.matches(out, bm) {
			return out
		}
	}
	c.issues = c.issues[:base]
	return v
}

// matches reports whether v already has the JSON type (and enum
// membership) bm declares.
func (c *conformer) matches(v interface{}, bm map[string]interface{}) bool {
	types := schemaTypes(bm)
	ok := len(types) == 0
	for _, t := range types {
		switch t {
		case "string":
			_, isS := v.(string)
			ok = ok || isS
		case "boolean":
			_, isB := v.(bool)
			ok = ok || isB
		case "integer":
			if n, isN := v.(json.Number); isN {
				_, err := strconv.ParseInt(n.String(), 10, 64)
				ok = ok || err == nil
			}
		case "number":
			_, isN := v.(json.Number)
			ok = ok || isN
		case "array":
			_, isA := v.([]interface{})
			ok = ok || isA
		case "object":
			_, isO := v.(map[string]interface{})
			ok = ok || isO
		default:
			ok = true
		}
	}
	if !ok {
		return false
	}
	if members := enumMembers(bm); members != nil {
		return enumIndex(members, v) >= 0
	}
	return true
}

func enumMembers(s map[string]interface{}) []interface{} {
	if en, ok := s["enum"].([]interface{}); ok && len(en) > 0 {
		return en
	}
	if k, ok := s["const"]; ok {
		return []interface{}{k}
	}
	return nil
}

func scalarText(v interface{}) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return x.String(), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

func enumIndex(members []interface{}, v interface{}) int {
	vt, ok := scalarText(v)
	if !ok {
		return -1
	}
	_, vIsStr := v.(string)
	for i, m := range members {
		if mt, ok := scalarText(m); ok && mt == vt {
			if _, mIsStr := m.(string); mIsStr == vIsStr {
				return i
			}
		}
	}
	return -1
}

func normEnum(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer("-", "_", " ", "_").Replace(s)
}

// enumFix repairs a near-miss enum value when the correction is
// unambiguous ("in-progress" -> "in_progress"); otherwise records an issue.
func (c *conformer) enumFix(v interface{}, s map[string]interface{}, path string) interface{} {
	members := enumMembers(s)
	if members == nil {
		return v
	}
	if enumIndex(members, v) >= 0 {
		return v
	}
	if vt, ok := scalarText(v); ok {
		hit := -1
		for i, m := range members {
			if mt, ok := scalarText(m); ok && (mt == vt || normEnum(mt) == normEnum(vt)) {
				if hit >= 0 {
					hit = -2
					break
				}
				hit = i
			}
		}
		if hit >= 0 {
			return members[hit]
		}
	}
	c.issues = append(c.issues, fmt.Sprintf("%s: value %v not in enum", pathOrRoot(path), v))
	return v
}

func pathOrRoot(p string) string {
	if p == "" {
		return "(root)"
	}
	return p
}
