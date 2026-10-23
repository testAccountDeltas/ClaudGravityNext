// Part of the translate package — see translate.go for the package doc
// comment. Direct port of v1's translator.go Step 6 (Tools Declaration).
package translate

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"claudgravity/internal/config"
	"claudgravity/internal/loopguard"
	"claudgravity/internal/schema"
	"claudgravity/internal/toolschema"
)

// applyToolsDeclaration builds Gemini's tools.0.functionDeclarations (or
// tools.0.googleSearch, mutually exclusive) and toolConfig from root's
// "tools"/"tool_choice", reusing tn's disambiguation decision computed up
// front so history replay and this declaration can never disagree.
// Returns the updated out, plus syntheticReasonTools (which tools got our
// synthetic "reason" placeholder injected, so the caller can strip it
// back out of the model's actual args) and reverseToolNameMap (sanitized
// name -> original, so the response path can restore the name Claude
// Code expects once Gemini invokes the function by its sanitized one).
func applyToolsDeclaration(out string, root gjson.Result, tenant config.TenantConfig, tn toolNameInfo, sessionID string) (string, map[string]bool, map[string]string) {
	syntheticReasonTools := make(map[string]bool)
	reverseToolNameMap := make(map[string]string)
	sentToolNames := make(map[string]string) // original name -> sanitized (Gemini-bound) name
	sentOriginals := make(map[string]string) // only original tool names -> sanitized
	editorName, bashName := "", ""

	// A client naming a tool via tool_choice has already said it
	// specifically needs that exact tool; honor it over the
	// strip_search_tools default instead of silently ignoring both.
	forcedToolName := ""
	if tc := root.Get("tool_choice"); tc.Get("type").String() == "tool" {
		forcedToolName = tc.Get("name").String()
	}

	tools := root.Get("tools")
	if !tools.IsArray() {
		return out, syntheticReasonTools, reverseToolNameMap
	}

	var decls []string
	hasWebSearch := false

	tools.ForEach(func(_, t gjson.Result) bool {
		name, _ := resolveToolName(t)
		// Claude Code's real native tool is snake_case "web_search" —
		// "WebSearch"/"GoogleSearch" never matched it, so inline Gemini
		// googleSearch grounding never actually activated for the one
		// real tool name it exists for.
		if isServerSearchTool(t, name) {
			hasWebSearch = true
			return true
		}
		if isUnsupportedServerTool(t, name) {
			logDroppedServerTool(name, t.Get("type").String())
			return true
		}
		if tenant.StripSearchTools && DropSearchTools[name] && name != forcedToolName && !tn.keepSearchToolsForForcedAny {
			return true
		}
		desc := t.Get("description").String()
		// GAP-T4: a computer-use tool's display_width_px/height_px/number
		// are declared on the TOOL OBJECT ITSELF, not inside input_schema —
		// Gemini's own tool-declaration shape has nowhere to put them at
		// all, so without surfacing them somewhere the model has no idea
		// what resolution it's clicking/typing against and hallucinates
		// coordinates. The description is the only channel left.
		if w, h := t.Get("display_width_px").Int(), t.Get("display_height_px").Int(); w > 0 && h > 0 {
			desc = strings.TrimRight(desc, "\n") + fmt.Sprintf("\n\nDisplay resolution: %dx%d pixels.", w, h)
			if num := t.Get("display_number").Int(); num > 0 {
				desc += fmt.Sprintf(" Display number: %d.", num)
			}
		}
		if name == "Read" {
			// Static text, same on every request, so it doesn't disturb
			// Gemini's prefix cache — sits right next to the schema the
			// model is already attending to when it decides how to call
			// THIS tool, instead of competing with everything else in the
			// system prompt.
			desc = strings.TrimRight(desc, "\n") + "\n\n" + loopguard.ReadToolGuidance
		}
		rawSchema := t.Get("input_schema").Raw
		// Anthropic's server-side built-in tools (bash_20250124,
		// text_editor_20250728, computer_20250124, and their earlier
		// dated variants) are declared by the client WITHOUT input_schema
		// at all — Anthropic's own server already knows their parameters
		// — but this gateway has no equivalent knowledge, so an empty
		// input_schema would otherwise fall straight into the generic
		// "no params, needsPlaceholder" path below, telling Gemini the
		// tool takes only a synthetic "reason" string.
		if strings.TrimSpace(rawSchema) == "" || rawSchema == "null" || rawSchema == "{}" {
			toolType := t.Get("type").String()
			if builtin, ok := toolschema.Builtin(toolType); ok {
				rawSchema = builtin
			} else if builtin, ok := toolschema.Builtin(name); ok {
				rawSchema = builtin
			}
		}
		schemaRaw := schema.CleanJSONSchemaForAntigravity(sessionID, name, rawSchema)
		needsPlaceholder := strings.TrimSpace(schemaRaw) == "" || schemaRaw == "null"
		if !needsPlaceholder {
			// A schema like "{}" or "{\"properties\":{}}" — no "type"
			// field at all — also needs the placeholder, same real
			// defect as the check above, just a different raw string
			// shape.
			propsVal := gjson.Get(schemaRaw, "properties")
			propsEmpty := !propsVal.Exists() || (propsVal.IsObject() && len(propsVal.Map()) == 0)
			schemaType := gjson.Get(schemaRaw, "type").String()
			if propsEmpty && (schemaType == "" || schemaType == "object") {
				needsPlaceholder = true
			}
		}
		if needsPlaceholder {
			// Google rejects an OBJECT-type schema with empty properties
			// ("parameters.properties: should be non-empty for OBJECT
			// type") — route through the same placeholder mechanism
			// CleanJSONSchemaForAntigravity already uses for schemas that
			// end up empty after cleaning, instead of a raw
			// properties:{} that bypasses it entirely.
			schemaRaw, _ = schema.AddEmptySchemaPlaceholder(`{"type":"object","properties":{}}`, nil)
		}
		// Remember which tools got our synthetic "reason" placeholder
		// injected at the TOP LEVEL (the only way Gemini will accept an
		// argument-less tool's schema) so the caller can strip that key
		// back out of the model's actual function-call args before
		// handing them to Claude Code — a strict MCP tool with
		// additionalProperties: false rejects a "reason" field it never
		// declared. Checks the exact top-level path rather than a
		// substring search across the whole schema, since a nested
		// free-dict field can independently carry the identical
		// placeholder text for its own emptiness.
		if schema.HasTopLevelPlaceholderReason(schemaRaw) {
			syntheticReasonTools[name] = true
		}

		// GAP-CGN-D2-02 (GAP-D2-12): Gemini requires parameters to be strictly of type OBJECT.
		// If a custom tool defines an input_schema with a scalar/array root, wrap it in an object.
		if gjson.Get(schemaRaw, "type").String() != "object" {
			schemaRaw = fmt.Sprintf(`{"type":"object","properties":{"__scalar_arg__":%s},"required":["__scalar_arg__"]}`, schemaRaw)
		}

		sanitizedName, ok := tn.disambiguatedToolName[name]
		if !ok {
			sanitizedName = sanitizeGeminiFunctionName(name)
		}
		if sanitizedName != name {
			reverseToolNameMap[sanitizedName] = name
		}
		decl := `{"name":"","description":"","parameters":{}}`
		decl, _ = sjson.Set(decl, "name", sanitizedName)
		decl, _ = sjson.Set(decl, "description", desc)
		decl, _ = sjson.SetRaw(decl, "parameters", schemaRaw)
		decls = append(decls, decl)
		sentToolNames[name] = sanitizedName
		sentOriginals[name] = sanitizedName
		sentToolNames[sanitizedName] = sanitizedName
		if toolType := t.Get("type").String(); toolType != "" {
			sentToolNames[toolType] = sanitizedName
		}
		if name == "str_replace_editor" || name == "str_replace_based_edit_tool" || strings.HasPrefix(t.Get("type").String(), "text_editor_") || t.Get("type").String() == "text_editor" {
			sentToolNames["text_editor"] = sanitizedName
			sentToolNames["str_replace_editor"] = sanitizedName
			editorName = name
		}
		if name == "bash" || strings.HasPrefix(t.Get("type").String(), "bash_") || t.Get("type").String() == "bash" {
			sentToolNames["bash"] = sanitizedName
			bashName = name
		}
		return true
	})

	for alias, orig := range repairedNameAliases(sentOriginals, editorName, bashName) {
		if _, exists := reverseToolNameMap[alias]; !exists {
			reverseToolNameMap[alias] = orig
		}
	}

	if hasWebSearch && len(decls) == 0 {
		out, _ = sjson.SetRaw(out, "tools.0.googleSearch", "{}")
	}

	if len(decls) > 0 {
		out, _ = sjson.SetRaw(out, "tools.0.functionDeclarations", "["+strings.Join(decls, ",")+"]")
	}

	// tool_choice: a client forcing tool use (type:"any") or a specific
	// tool (type:"tool", name:X) must not silently fall back to Gemini's
	// own default "let the model decide".
	if len(decls) > 0 {
		if tc := root.Get("tool_choice"); tc.Exists() {
			// GAP-TL-10: tool_choice can arrive as a bare JSON string
			// ("auto"/"any"/"none"), not just the object form
			// ({"type":"any"}) — tc.Get("type") on a string value has no
			// sub-path and returns empty, so the shorthand was silently
			// ignored. Read the choice value itself when tc isn't an
			// object.
			choiceType := tc.Get("type").String()
			if tc.Type == gjson.String {
				choiceType = tc.String()
			}
			switch choiceType {
			case "auto":
				out, _ = sjson.Set(out, "toolConfig.functionCallingConfig.mode", "AUTO")
			case "any", "required":
				out, _ = sjson.Set(out, "toolConfig.functionCallingConfig.mode", "ANY")
			case "tool":
				if name := tc.Get("name").String(); name != "" {
					if _, isFn := sentToolNames[name]; !isFn && hasWebSearch {
						{
							// GAP-R17-06: Gemini strictly forbids functionDeclarations and googleSearch in the same Tool
							out, _ = sjson.Delete(out, "tools.0.functionDeclarations")
							out, _ = sjson.Delete(out, "toolConfig")
							out, _ = sjson.SetRaw(out, "tools.0.googleSearch", "{}")
						}
					} else if sanitized, ok := sentToolNames[name]; ok {
						// allowedFunctionNames must reference the
						// SANITIZED (Gemini-bound) name, not the original
						// Claude-side one, or Gemini 400s with
						// "allowed_function_names contains X which is not
						// in function_declarations" the moment the
						// original name had a hyphen.
						out, _ = sjson.Set(out, "toolConfig.functionCallingConfig.mode", "ANY")
						out, _ = sjson.Set(out, "toolConfig.functionCallingConfig.allowedFunctionNames.0", sanitized)
					}
				}
			case "none":
				out, _ = sjson.Set(out, "toolConfig.functionCallingConfig.mode", "NONE")
			}
		}
	}

	return out, syntheticReasonTools, reverseToolNameMap
}
