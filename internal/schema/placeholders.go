// Part of the schema package — see jsonpath.go for the package doc
// comment. Direct port of v1's addEmptySchemaPlaceholder/
// removePlaceholderFields/stripNestedOpenDictReason (and its two helpers)
// plus the openDictArgFields cache CleanJSONSchemaForAntigravity (clean.go)
// populates and this file's own stripNestedOpenDictReason reads.
package schema

import (
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/ephemeral"
)

const placeholderReasonDescription = "Brief explanation of why you are calling this tool"

const (
	openDictArgStaleAfter = 2 * time.Hour
	openDictArgSweepEvery = 30 * time.Minute
)

// openDictArgFields remembers, per session+tool, which top-level
// argument fields are free-form dicts (originally declared with
// additionalProperties, e.g. Bash's "env" or TaskCreate's "metadata") that
// addEmptySchemaPlaceholder had to inject a "reason" placeholder into — so
// a function call's args can get that nested "env.reason" stripped too,
// not just a top-level "reason".
//
// An audit caught this used to be a bare package-level map keyed ONLY by
// toolName, shared across every concurrent session/tenant — two sessions
// declaring a same-named tool with different schemas would overwrite each
// other's entry, same cross-request leak as numericenum.go's cache (see
// its own comment). Switched to internal/ephemeral.Store keyed by
// sessionID+toolName for the same reason.
var openDictArgFields = ephemeral.New[[]string](openDictArgStaleAfter, openDictArgSweepEvery)

func openDictArgKey(sessionID, toolName string) string { return sessionID + "\x00" + toolName }

// setOpenDictArgFields is clean.go's own write-side of the cache above —
// kept here, alongside the matching read-side (stripNestedOpenDictReason),
// rather than in clean.go, so one file owns the whole cache.
func setOpenDictArgFields(sessionID, toolName string, fields []string) {
	key := openDictArgKey(sessionID, toolName)
	if len(fields) > 0 {
		openDictArgFields.Set(key, fields)
	} else {
		openDictArgFields.Delete(key)
	}
}

// StripNestedOpenDictReason strips a synthetic "reason" placeholder from
// every free-form-dict field recorded for sessionID+toolName, not just
// the top level.
func StripNestedOpenDictReason(sessionID, toolName, argsRaw string) string {
	fields, _ := openDictArgFields.Get(openDictArgKey(sessionID, toolName))
	for _, field := range fields {
		argsRaw = stripReasonAtFieldSpec(argsRaw, "", field)
	}
	return argsRaw
}

// stripReasonAtFieldSpec walks a single openDictArgFields entry against
// the real argument VALUES, deleting a synthetic "reason" placeholder
// wherever the spec resolves. The spec can contain any number of
// ".items"/".items." array markers (addEmptySchemaPlaceholder's own
// convention for "this placeholder sat inside an array's element schema",
// possibly nested several arrays deep — e.g. "matrix.items.items.metadata"
// for a 2D array of arrays of objects with a "metadata" dict); this peels
// off one marker at a time via stripReasonAcrossArray's own recursion,
// rather than flattening the whole spec into one string path after only
// the first marker (which would leave a second/deeper dimension's own
// "items" token as a literal, unmatchable path segment).
// pathPrefix is the real gjson/sjson path already resolved so far (starts
// empty at the top level, grows by one concrete array index per
// recursion); field is the remaining, not-yet-resolved tail of the spec.
func stripReasonAtFieldSpec(argsRaw, pathPrefix, field string) string {
	joined := func(key string) string {
		if pathPrefix == "" {
			return key
		}
		if key == "" {
			return pathPrefix
		}
		return pathPrefix + "." + key
	}
	if field == "items" {
		return stripReasonAcrossArray(argsRaw, joined(""), "")
	}
	if rest, ok := strings.CutPrefix(field, "items."); ok {
		return stripReasonAcrossArray(argsRaw, joined(""), rest)
	}
	if arrayField, ok := strings.CutSuffix(field, ".items"); ok {
		return stripReasonAcrossArray(argsRaw, joined(arrayField), "")
	}
	if idx := strings.Index(field, ".items."); idx != -1 {
		arrayField := field[:idx]
		remainder := field[idx+len(".items."):]
		return stripReasonAcrossArray(argsRaw, joined(arrayField), remainder)
	}
	// No more array markers — a plain nested-object path, delete "reason"
	// directly under it.
	path := joined(field) + ".reason"
	if gjson.Get(argsRaw, path).Exists() {
		if cleaned, err := sjson.Delete(argsRaw, path); err == nil {
			argsRaw = cleaned
		}
	}
	return argsRaw
}

// stripReasonAcrossArray iterates every element of the array at
// arrayPath, recursing into stripReasonAtFieldSpec for each one with
// remainder as the still-unresolved tail — remainder may itself contain
// further array markers for additional dimensions.
func stripReasonAcrossArray(argsRaw, arrayPath, remainder string) string {
	arr := gjson.Get(argsRaw, arrayPath)
	if !arr.IsArray() {
		return argsRaw
	}
	n := len(arr.Array())
	for i := n - 1; i >= 0; i-- {
		elemPrefix := fmt.Sprintf("%s.%d", arrayPath, i)
		if remainder == "" {
			path := elemPrefix + ".reason"
			if gjson.Get(argsRaw, path).Exists() {
				if cleaned, err := sjson.Delete(argsRaw, path); err == nil {
					argsRaw = cleaned
				}
			}
			continue
		}
		argsRaw = stripReasonAtFieldSpec(argsRaw, elemPrefix, remainder)
	}
	return argsRaw
}

// removePlaceholderFields removes placeholder-only properties ("_" and
// "reason") and their required entries — used only on the
// CleanJSONSchemaForGemini (Structured Outputs) path, never on tool
// declarations, which need the placeholder to satisfy Claude VALIDATED
// mode's "at least one required property" rule.
func removePlaceholderFields(jsonStr string) string {
	paths := findPaths(jsonStr, "_")
	sortByDepth(paths)
	for _, p := range paths {
		if p != "properties._" && !strings.HasSuffix(p, ".properties._") {
			continue
		}
		jsonStr, _ = sjson.Delete(jsonStr, p)
		parentPath := trimSuffix(p, ".properties._")
		reqPath := joinPath(parentPath, "required")
		req := gjson.Get(jsonStr, reqPath)
		if req.IsArray() {
			var filtered []string
			for _, r := range req.Array() {
				if r.String() != "_" {
					filtered = append(filtered, r.String())
				}
			}
			if len(filtered) == 0 {
				jsonStr, _ = sjson.Delete(jsonStr, reqPath)
			} else {
				jsonStr, _ = sjson.Set(jsonStr, reqPath, filtered)
			}
		}
	}

	reasonPaths := findPaths(jsonStr, "reason")
	sortByDepth(reasonPaths)
	for _, p := range reasonPaths {
		if p != "properties.reason" && !strings.HasSuffix(p, ".properties.reason") {
			continue
		}
		parentPath := trimSuffix(p, ".properties.reason")
		props := gjson.Get(jsonStr, joinPath(parentPath, "properties"))
		if !props.IsObject() || len(props.Map()) != 1 {
			continue
		}
		desc := gjson.Get(jsonStr, p+".description").String()
		if desc != placeholderReasonDescription && desc != "Placeholder" {
			continue
		}
		jsonStr, _ = sjson.Delete(jsonStr, p)
		reqPath := joinPath(parentPath, "required")
		req := gjson.Get(jsonStr, reqPath)
		if req.IsArray() {
			var filtered []string
			for _, r := range req.Array() {
				if r.String() != "reason" {
					filtered = append(filtered, r.String())
				}
			}
			if len(filtered) == 0 {
				jsonStr, _ = sjson.Delete(jsonStr, reqPath)
			} else {
				jsonStr, _ = sjson.Set(jsonStr, reqPath, filtered)
			}
		}
	}

	return jsonStr
}

// fixEmptyObjectSchemasForResponseSchema ensures that on Structured Outputs
// (CleanJSONSchemaForGemini / responseSchema), any object node that has no
// properties doesn't get rejected by Gemini ("properties should be non-empty for OBJECT type").
func fixEmptyObjectSchemasForResponseSchema(jsonStr string) string {
	paths := findPaths(jsonStr, "type")
	sortByDepth(paths)
	for _, p := range paths {
		if strings.HasPrefix(p, "contents.") {
			continue
		}
		if gjson.Get(jsonStr, p).String() != "object" {
			continue
		}
		parentPath := trimSuffix(p, ".type")
		propsPath := joinPath(parentPath, "properties")
		props := gjson.Get(jsonStr, propsPath)
		if !props.Exists() || (props.IsObject() && len(props.Map()) == 0) {
			if parentPath == "" {
				jsonStr, _ = sjson.Set(jsonStr, "properties.result.type", "string")
			} else {
				jsonStr, _ = sjson.Set(jsonStr, p, "string")
				jsonStr, _ = sjson.Delete(jsonStr, propsPath)
				descPath := joinPath(parentPath, "description")
				if !gjson.Get(jsonStr, descPath).Exists() {
					jsonStr, _ = sjson.Set(jsonStr, descPath, "JSON object")
				}
			}
		}
	}
	return jsonStr
}

// addEmptySchemaPlaceholder adds a placeholder "reason" property to empty
// object schemas — Claude VALIDATED mode requires at least one required
// property in tool schemas, and a genuinely empty properties object is
// the one case Google's own validator actually rejects without it
// ("parameters.properties: should be non-empty for OBJECT type").
// nestedOpenDictFields collects the top-level argument field name (e.g.
// "env") of every NESTED (non-root) object that got the placeholder
// because it was actually a free-form dict (openDictPaths) rather than a
// genuinely empty schema, so the caller can register them for recursive
// "reason" cleanup via StripNestedOpenDictReason on the way back from a
// function call.
//
// openDictPaths is accepted but, matching v1 exactly, never actually read
// in this function's body — "needsPlaceholder" below is determined purely
// from the node's own properties shape. Kept as a parameter anyway rather
// than silently dropped, since removing it would be a real signature
// change this port isn't meant to make; flagged here as a found-by-reading
// v1 oddity, not something this port invented.
// AddEmptySchemaPlaceholder exports addEmptySchemaPlaceholder for
// internal/translate's own "needsPlaceholder" case (a tool declaration
// whose schema is a bare OBJECT with no properties at all, e.g. a
// built-in Anthropic tool with no input_schema and no reference schema
// on file) — Google rejects an OBJECT-type schema with empty properties
// outright, and this is the one mechanism CleanJSONSchemaForAntigravity
// itself already uses for that case.
func AddEmptySchemaPlaceholder(jsonStr string, openDictPaths map[string]bool) (string, []string) {
	return addEmptySchemaPlaceholder(jsonStr, openDictPaths)
}

// HasTopLevelPlaceholderReason reports whether schemaRaw's top-level
// "reason" property is exactly the synthetic placeholder
// addEmptySchemaPlaceholder injects — internal/translate uses this to
// decide whether a tool call's own synthetic "reason" argument needs to
// be stripped back out before reaching Claude Code, checking the exact
// top-level path rather than a substring search across the whole schema
// (a nested free-dict field can independently carry the identical
// placeholder text for its own emptiness, which a substring search would
// wrongly also match against this tool's top level).
func HasTopLevelPlaceholderReason(schemaRaw string) bool {
	desc := gjson.Get(schemaRaw, "properties.reason.description").String()
	return desc == placeholderReasonDescription || desc == "Placeholder"
}

func addEmptySchemaPlaceholder(jsonStr string, openDictPaths map[string]bool) (string, []string) {
	trimmed := strings.TrimSpace(jsonStr)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" || (gjson.Parse(trimmed).IsObject() && len(gjson.Parse(trimmed).Map()) == 0) {
		return fmt.Sprintf(`{"type":"object","properties":{"reason":{"type":"string","description":"%s"}},"required":["reason"]}`, placeholderReasonDescription), nil
	}
	if !gjson.Get(jsonStr, "type").Exists() && !gjson.Get(jsonStr, "properties").Exists() {
		jsonStr, _ = sjson.Set(jsonStr, "type", "object")
	}
	paths := findPaths(jsonStr, "type")
	sortByDepth(paths) // process deepest to shallowest

	var nestedOpenDictFields []string
	for _, p := range paths {
		typeVal := gjson.Get(jsonStr, p)
		if typeVal.String() != "object" {
			continue
		}

		parentPath := trimSuffix(p, ".type")
		propsPath := joinPath(parentPath, "properties")
		propsVal := gjson.Get(jsonStr, propsPath)
		reqPath := joinPath(parentPath, "required")

		needsPlaceholder := false
		if !propsVal.Exists() {
			needsPlaceholder = true
		} else if propsVal.IsObject() && len(propsVal.Map()) == 0 {
			needsPlaceholder = true
		}

		if needsPlaceholder {
			reasonPath := joinPath(propsPath, "reason")
			jsonStr, _ = sjson.Set(jsonStr, reasonPath+".type", "string")
			jsonStr, _ = sjson.Set(jsonStr, reasonPath+".description", placeholderReasonDescription)
			jsonStr, _ = sjson.Set(jsonStr, reqPath, []string{"reason"})

			if field, ok := strings.CutPrefix(parentPath, "properties."); ok {
				// Keep field in its already-escaped form (do not unescape a
				// literal "." in a property name) — stripReasonAtFieldSpec
				// builds a gjson/sjson path by plain string concatenation,
				// and both libraries treat an unescaped "." as a path
				// separator, so an unescaped field name would silently
				// never match on the way back.
				argPath := strings.ReplaceAll(field, ".properties.", ".")
				nestedOpenDictFields = append(nestedOpenDictFields, argPath)
			}
		}
		// Does NOT force-add a bogus "_": {"type":"boolean"} required
		// property to any nested object that has real properties but none
		// marked required — not something Google's API has ever been
		// confirmed to reject, and it pollutes the arguments the model
		// actually fills in.
	}

	return jsonStr, nestedOpenDictFields
}
