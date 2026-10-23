// Part of the schema package — see jsonpath.go for the package doc
// comment. Direct port of v1's convertRefsToHints/resolveLocalRef.
package schema

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// convertRefsToHints converts $ref to description hints (lazy-hint
// strategy): a local $ref ("#/$defs/...", "#/definitions/...",
// "#/components/schemas/...") is inlined via resolveLocalRef; anything
// else degrades to a "See: <name>" description hint, since Gemini's
// protobuf Schema has no $ref concept at all.
func convertRefsToHints(jsonStr string) string {
	expandedRefPaths := make(map[string]string) // prefixPath -> refVal

	for pass := 0; pass < 20; pass++ {
		paths := findPaths(jsonStr, "$ref")
		if len(paths) == 0 {
			break
		}
		sortByDepth(paths)

		for _, p := range paths {
			refNode := gjson.Get(jsonStr, p)
			if !refNode.Exists() {
				continue
			}
			refVal := refNode.String()
			if refVal == "" {
				continue
			}
			parentPath := trimSuffix(p, ".$ref")

			defName := refVal
			if idx := strings.LastIndex(refVal, "/"); idx >= 0 {
				defName = refVal[idx+1:]
			}

			// True recursion check: is this $ref being evaluated inside an
			// ancestor path that already expanded the exact same refVal?
			isRecursive := false
			for prefix, expandedRef := range expandedRefPaths {
				if expandedRef == refVal {
					if prefix == "" || parentPath == prefix || strings.HasPrefix(parentPath, prefix+".") {
						isRecursive = true
						break
					}
				}
			}

			if isRecursive {
				replacement := fmt.Sprintf(`{"type":"object","description":"Recursive reference: See %s"}`, defName)
				jsonStr = setRawAt(jsonStr, parentPath, replacement)
				continue
			}

			if def := resolveLocalRef(jsonStr, refVal); def != "" {
				expandedRefPaths[parentPath] = refVal
				if existingDesc := gjson.Get(jsonStr, descriptionPath(parentPath)).String(); existingDesc != "" {
					def, _ = sjson.Set(def, "description", existingDesc)
				}
				// If replacing at root, preserve $defs/definitions/components
				// so subsequent passes can resolve nested refs.
				if parentPath == "" {
					if !gjson.Get(def, "$defs").Exists() && gjson.Get(jsonStr, "$defs").Exists() {
						def, _ = sjson.SetRaw(def, "$defs", gjson.Get(jsonStr, "$defs").Raw)
					}
					if !gjson.Get(def, "definitions").Exists() && gjson.Get(jsonStr, "definitions").Exists() {
						def, _ = sjson.SetRaw(def, "definitions", gjson.Get(jsonStr, "definitions").Raw)
					}
					if !gjson.Get(def, "components").Exists() && gjson.Get(jsonStr, "components").Exists() {
						def, _ = sjson.SetRaw(def, "components", gjson.Get(jsonStr, "components").Raw)
					}
				}
				jsonStr = setRawAt(jsonStr, parentPath, def)
				continue
			}

			hint := fmt.Sprintf("See: %s", defName)
			if existing := gjson.Get(jsonStr, descriptionPath(parentPath)).String(); existing != "" {
				hint = fmt.Sprintf("%s (%s)", existing, hint)
			}

			replacement := `{"type":"object","description":""}`
			replacement, _ = sjson.Set(replacement, "description", hint)
			jsonStr = setRawAt(jsonStr, parentPath, replacement)
		}
	}
	return jsonStr
}

// resolveLocalRef looks up a JSON Schema $ref against this same
// document's own "$defs"/"definitions"/"components.schemas" dictionary —
// the only locations this function understands; a remote/external $ref
// URL falls back to convertRefsToHints' lossy hint-stub. Returns "" if
// refVal isn't a local pointer or the named definition doesn't exist.
func resolveLocalRef(jsonStr, refVal string) string {
	const defsPrefix = "#/$defs/"
	const definitionsPrefix = "#/definitions/"
	const componentsSchemasPrefix = "#/components/schemas/"

	var prefix, rootField string
	switch {
	case strings.HasPrefix(refVal, defsPrefix):
		prefix, rootField = defsPrefix, "$defs"
	case strings.HasPrefix(refVal, definitionsPrefix):
		prefix, rootField = definitionsPrefix, "definitions"
	case strings.HasPrefix(refVal, componentsSchemasPrefix):
		prefix, rootField = componentsSchemasPrefix, "components.schemas"
	default:
		return ""
	}

	subPath := refVal[len(prefix):]
	segments := strings.Split(subPath, "/")
	currPath := rootField
	for _, seg := range segments {
		seg = strings.ReplaceAll(seg, "~1", "/")
		seg = strings.ReplaceAll(seg, "~0", "~")
		currPath = currPath + "." + escapeGJSONPathKey(seg)
	}
	if def := gjson.Get(jsonStr, currPath); def.Exists() {
		return def.Raw
	}
	return ""
}
