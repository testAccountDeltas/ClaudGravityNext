// Package schema ports superrelay (v1)'s gemini_schema.go (2643 lines) —
// the JSON Schema cleanup pipeline that rewrites a Claude tool's
// input_schema (or a Structured Outputs responseSchema) into the subset
// Gemini's protobuf Schema actually accepts. Unlike internal/sanitize and
// internal/tokens, this package deliberately stays string/gjson/sjson-
// based rather than moving to a typed domain: a JSON Schema's own shape is
// inherently dynamic (arbitrary nesting, vendor extensions, draft
// differences — exactly why internal/protocol/claude's own Tool.InputSchema
// is json.RawMessage, not a fixed struct), so a path-based transformation
// pipeline is the right tool here, not a rewrite. The port is a near-
// literal transcription split into files by responsibility (matching the
// migration spec's Part 5, loosely — two files split further to stay
// under the 400-line ceiling, noted in ROADMAP_PROGRESS.md), not a
// behavior rewrite: this file is the single most audit-scarred file in
// the codebase (dozens of documented one-line edge-case fixes), and the
// safest way to carry that forward is to change as little as possible
// about the actual algorithms.
//
// This file holds the pure gjson/sjson path utilities every other file in
// this package depends on.
package schema

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var gjsonPathKeyReplacer = strings.NewReplacer(".", "\\.", "*", "\\*", "?", "\\?")

// findPaths returns every gjson path where a key named field appears
// anywhere in jsonStr.
func findPaths(jsonStr, field string) []string {
	var paths []string
	walk(gjson.Parse(jsonStr), "", field, &paths)
	return paths
}

func walk(value gjson.Result, path string, targetKey string, paths *[]string) {
	if value.IsArray() {
		for i, item := range value.Array() {
			walk(item, joinPath(path, fmt.Sprintf("%d", i)), targetKey, paths)
		}
		return
	}
	if value.IsObject() {
		value.ForEach(func(key, val gjson.Result) bool {
			k := key.String()
			childPath := joinPath(path, escapeGJSONPathKey(k))
			if k == targetKey {
				*paths = append(*paths, childPath)
			}
			walk(val, childPath, targetKey, paths)
			return true
		})
	}
}

// findPathsByFields returns, for each of fields, every path where that
// key appears — one single tree walk instead of one walk per field.
func findPathsByFields(jsonStr string, fields []string) map[string][]string {
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		set[field] = struct{}{}
	}
	paths := make(map[string][]string, len(set))
	walkForFields(gjson.Parse(jsonStr), "", set, paths)
	return paths
}

func walkForFields(value gjson.Result, path string, fields map[string]struct{}, paths map[string][]string) {
	if value.Type != gjson.JSON {
		return
	}
	if value.IsArray() {
		for i, item := range value.Array() {
			walkForFields(item, joinPath(path, fmt.Sprintf("%d", i)), fields, paths)
		}
		return
	}
	if value.IsObject() {
		value.ForEach(func(key, val gjson.Result) bool {
			keyStr := key.String()
			childPath := joinPath(path, escapeGJSONPathKey(keyStr))
			if _, ok := fields[keyStr]; ok {
				paths[keyStr] = append(paths[keyStr], childPath)
			}
			walkForFields(val, childPath, fields, paths)
			return true
		})
	}
}

// sortByDepth orders paths deepest-first (then longest-first on a depth
// tie) so a caller deleting/rewriting them processes nested paths before
// their ancestors — mutating a parent first would invalidate every child
// path string still queued.
func sortByDepth(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		depthI := strings.Count(paths[i], ".")
		depthJ := strings.Count(paths[j], ".")
		if depthI != depthJ {
			return depthI > depthJ
		}
		return len(paths[i]) > len(paths[j])
	})
}

func trimSuffix(path, suffix string) string {
	if path == strings.TrimPrefix(suffix, ".") {
		return ""
	}
	return strings.TrimSuffix(path, suffix)
}

func joinPath(base, suffix string) string {
	if base == "" {
		return suffix
	}
	return base + "." + suffix
}

// setRawAt sets value (raw JSON) at path, replacing the whole document
// when path is the schema root.
func setRawAt(jsonStr, path, value string) string {
	if path == "" {
		return value
	}
	result, _ := sjson.SetRaw(jsonStr, path, value)
	return result
}

// isPropertyDefinition reports whether path is a schema "properties"
// container object (where its immediate children are property definitions),
// rather than a property that happens to be named "properties".
// When a property itself is named "properties" (e.g. "properties.properties"),
// its path ends with ".properties", but it is a property definition, NOT a container.
// We distinguish this by checking that the consecutive trailing "properties"
// segments count is odd (1, 3, ...), which marks a container, whereas an even count
// (2, 4, ...) marks a property named "properties".
func isPropertyDefinition(path string) bool {
	if path == "properties" {
		return true
	}
	parts := splitGJSONPath(path)
	if len(parts) == 0 || parts[len(parts)-1] != "properties" {
		return false
	}
	consecutiveProperties := 0
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] == "properties" {
			consecutiveProperties++
		} else {
			break
		}
	}
	return consecutiveProperties%2 != 0
}

func descriptionPath(parentPath string) string {
	if parentPath == "" || parentPath == "@this" {
		return "description"
	}
	return parentPath + ".description"
}

// appendHint appends hint to parentPath's own "description", parenthesized
// after whatever description already exists there.
func appendHint(jsonStr, parentPath, hint string) string {
	descPath := descriptionPath(parentPath)
	existing := gjson.Get(jsonStr, descPath).String()
	if existing != "" {
		hint = fmt.Sprintf("%s (%s)", existing, hint)
	}
	jsonStr, _ = sjson.Set(jsonStr, descPath, hint)
	return jsonStr
}

// appendHintRaw is appendHint's counterpart for a standalone schema
// fragment (jsonRaw) that hasn't been spliced into the document yet.
func appendHintRaw(jsonRaw, hint string) string {
	existing := gjson.Get(jsonRaw, "description").String()
	if existing != "" {
		hint = fmt.Sprintf("%s (%s)", existing, hint)
	}
	jsonRaw, _ = sjson.Set(jsonRaw, "description", hint)
	return jsonRaw
}

func mergeDescriptionRaw(schemaRaw, parentDesc string) string {
	childDesc := gjson.Get(schemaRaw, "description").String()
	switch {
	case childDesc == "":
		schemaRaw, _ = sjson.Set(schemaRaw, "description", parentDesc)
		return schemaRaw
	case childDesc == parentDesc:
		return schemaRaw
	default:
		combined := fmt.Sprintf("%s (%s)", parentDesc, childDesc)
		schemaRaw, _ = sjson.Set(schemaRaw, "description", combined)
		return schemaRaw
	}
}

func getStrings(jsonStr, path string) []string {
	var result []string
	if arr := gjson.Get(jsonStr, path); arr.IsArray() {
		for _, r := range arr.Array() {
			result = append(result, r.String())
		}
	}
	return result
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func orDefault(val, def string) string {
	if val == "" {
		return def
	}
	return val
}

func escapeGJSONPathKey(key string) string {
	if !strings.ContainsAny(key, ".*?") {
		return key
	}
	return gjsonPathKeyReplacer.Replace(key)
}

func unescapeGJSONPathKey(key string) string {
	if !strings.Contains(key, "\\") {
		return key
	}
	var b strings.Builder
	b.Grow(len(key))
	for i := 0; i < len(key); i++ {
		if key[i] == '\\' && i+1 < len(key) {
			i++
			b.WriteByte(key[i])
			continue
		}
		b.WriteByte(key[i])
	}
	return b.String()
}

// splitGJSONPath splits a gjson path into its dot-separated segments,
// respecting backslash-escaped dots within a segment (produced by
// escapeGJSONPathKey for a property name that itself contains a literal
// "." or "*"/"?").
func splitGJSONPath(path string) []string {
	if path == "" {
		return nil
	}
	parts := make([]string, 0, strings.Count(path, ".")+1)
	var b strings.Builder
	b.Grow(len(path))
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == '\\' && i+1 < len(path) {
			b.WriteByte('\\')
			i++
			b.WriteByte(path[i])
			continue
		}
		if c == '.' {
			parts = append(parts, b.String())
			b.Reset()
			continue
		}
		b.WriteByte(c)
	}
	parts = append(parts, b.String())
	return parts
}

// walkForExtensions collects every "x-..." extension field's path (OpenAPI/
// JSON Schema vendor extensions Google's APIs don't recognize), skipping
// the children of one it collects (a deleted node's own sub-fields need no
// separate path).
func walkForExtensions(value gjson.Result, path string, paths *[]string) {
	if value.IsArray() {
		arr := value.Array()
		for i := len(arr) - 1; i >= 0; i-- {
			walkForExtensions(arr[i], joinPath(path, strconv.Itoa(i)), paths)
		}
		return
	}
	if value.IsObject() {
		value.ForEach(func(key, val gjson.Result) bool {
			keyStr := key.String()
			childPath := joinPath(path, escapeGJSONPathKey(keyStr))
			if strings.HasPrefix(keyStr, "x-") && !isPropertyDefinition(path) {
				*paths = append(*paths, childPath)
				return true
			}
			walkForExtensions(val, childPath, paths)
			return true
		})
	}
}
