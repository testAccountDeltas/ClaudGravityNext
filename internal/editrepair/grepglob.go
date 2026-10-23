// Part of the editrepair package's port from superrelay (v1)'s
// edit_repair.go — see cwd.go for the split rationale.
//
// RepairGrepToolCall and RepairGlobToolCall catch the specific ways Gemini
// mangles these two tools' calls — confirmed independently in a sibling
// project (opencode, same Gemini models): the model reliably drops or
// misplaces the one required "pattern" field on a multi-field search tool,
// while a single general shell command string never has this problem. Not
// wired to any live behavior change by itself — strip_search_tools stays
// true by default until there's been real traffic through this repair pass
// to build confidence in it.
package editrepair

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// RepairGrepToolCall fixes: (1) the model passing a bare JSON string instead
// of an object — wrapped as {"pattern": <string>}; (2) a missing/empty
// "pattern" with the search term sitting in "path" instead (path containing
// glob/regex metacharacters, or no path separator at all, is the tell).
func RepairGrepToolCall(argsRaw string) (string, bool) {
	parsed := gjson.Parse(argsRaw)
	if !parsed.Exists() {
		return argsRaw, false
	}
	if parsed.Type == gjson.String {
		s := parsed.String()
		if s == "" {
			return argsRaw, false
		}
		fixed, _ := sjson.Set(`{}`, "pattern", s)
		return fixed, true
	}
	if !parsed.IsObject() {
		return argsRaw, false
	}

	pattern := parsed.Get("pattern").String()
	path := parsed.Get("path").String()
	if pattern != "" {
		return argsRaw, false
	}
	// An audit caught that a model calling Grep with {"path":"main.go",
	// "query":"HandleRequest"} (a plausible alias a weaker model reaches
	// for) fell straight into the "path looks like it's actually the
	// search term" heuristic below — "main.go" has no special regex
	// chars AND no slash, so it passed — repurposing the FILENAME as the
	// search pattern, deleting "path" entirely, and silently discarding
	// "query" (the model's real intended search term). Check for an
	// actual pattern-alias key first; only fall back to repurposing path
	// when none of those exist either.
	for _, aliasKey := range []string{"query", "search", "regex", "term", "text"} {
		if alias := parsed.Get(aliasKey).String(); alias != "" {
			fixed, _ := sjson.Set(argsRaw, "pattern", alias)
			fixed, _ = sjson.Delete(fixed, aliasKey)
			return fixed, true
		}
	}
	// An audit caught that "\\" in the first check (regex special chars)
	// falsely matched ANY Windows-style path at all — "\" is also a
	// literal path separator there, and "controllers\auth.go" has no
	// genuine regex metacharacter, just a normal Windows directory
	// separator. Keep "\\" in the SECOND check (no separator at all —
	// still correctly recognizes "\" as a valid separator there, so a
	// real Windows path doesn't also trip that one), but drop it from
	// the regex-metacharacter set.
	if path != "" && (strings.ContainsAny(path, "*?[]()|^$+") || !strings.ContainsAny(path, "/\\")) {
		fixed, _ := sjson.Set(argsRaw, "pattern", path)
		fixed, _ = sjson.Delete(fixed, "path")
		return fixed, true
	}
	return argsRaw, false
}

// RepairGlobToolCall fixes the same missing/misplaced "pattern" issue for
// Glob — its search term is a filename glob (e.g. "**/*.go") rather than a
// regex, so the "looks like a pattern" heuristic checks glob wildcards.
func RepairGlobToolCall(argsRaw string) (string, bool) {
	parsed := gjson.Parse(argsRaw)
	if !parsed.Exists() {
		return argsRaw, false
	}
	if parsed.Type == gjson.String {
		s := parsed.String()
		if s == "" {
			return argsRaw, false
		}
		fixed, _ := sjson.Set(`{}`, "pattern", s)
		return fixed, true
	}
	if !parsed.IsObject() {
		return argsRaw, false
	}

	pattern := parsed.Get("pattern").String()
	path := parsed.Get("path").String()
	if pattern != "" {
		return argsRaw, false
	}
	// An audit caught that, unlike RepairGrepToolCall just above, this had
	// no alias-key fallback at all — a model calling Glob(glob:"*.go")
	// (an entirely plausible alias given the tool's own name) left
	// "pattern" missing, the required-parameter validation on Claude
	// Code's side rejecting the call outright.
	for _, aliasKey := range []string{"glob", "query", "search"} {
		if alias := parsed.Get(aliasKey).String(); alias != "" {
			fixed, _ := sjson.Set(argsRaw, "pattern", alias)
			fixed, _ = sjson.Delete(fixed, aliasKey)
			return fixed, true
		}
	}
	if path != "" && strings.ContainsAny(path, "*?[]") {
		fixed, _ := sjson.Set(argsRaw, "pattern", path)
		fixed, _ = sjson.Delete(fixed, "path")
		return fixed, true
	}
	return argsRaw, false
}
