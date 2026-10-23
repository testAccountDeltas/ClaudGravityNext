// Part of the httpapi package — see errors.go for the package doc
// comment. Pagination, description decoration and normalised lookup for
// the Models API (GET /v1/models, GET /v1/models/{id}).
package httpapi

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"claudgravity/internal/accounts/tiering"
)

const (
	defaultModelsPageLimit = 20
	maxModelsPageLimit     = 1000
)

// decorateModel returns a copy of m with the fields the Anthropic Models
// API documents (and Claude Code's picker displays) filled in: a
// human-readable description next to display_name/capabilities.
func decorateModel(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	if d, _ := out["description"].(string); d == "" {
		name, _ := out["display_name"].(string)
		if name == "" {
			name, _ = out["id"].(string)
		}
		out["description"] = name + " (served by the ClaudGravity gateway)"
	}
	return out
}

// sortedModels orders the catalog newest first (created_at desc, id asc as
// the tiebreak), the order Anthropic's own list endpoint uses.
func sortedModels() []map[string]interface{} {
	models := allModels()
	sort.SliceStable(models, func(i, j int) bool {
		ci, _ := models[i]["created_at"].(string)
		cj, _ := models[j]["created_at"].(string)
		if ci != cj {
			return ci > cj
		}
		return fmt.Sprint(models[i]["id"]) < fmt.Sprint(models[j]["id"])
	})
	return models
}

// paginateModels applies limit/after_id/before_id the way the Anthropic
// API does. An invalid limit or an unknown cursor returns an error message.
func paginateModels(models []map[string]interface{}, limitStr, afterID, beforeID string) (page []map[string]interface{}, hasMore bool, errMsg string) {
	limit := defaultModelsPageLimit
	if limitStr != "" {
		n, err := strconv.Atoi(limitStr)
		if err != nil || n < 1 || n > maxModelsPageLimit {
			return nil, false, fmt.Sprintf("limit: must be an integer between 1 and %d", maxModelsPageLimit)
		}
		limit = n
	}
	indexOf := func(id string) int {
		for i, m := range models {
			if m["id"] == id {
				return i
			}
		}
		return -1
	}
	start, end := 0, len(models)
	if afterID != "" {
		i := indexOf(afterID)
		if i < 0 {
			return nil, false, "after_id: unknown model id " + afterID
		}
		start = i + 1
	}
	if beforeID != "" {
		i := indexOf(beforeID)
		if i < 0 {
			return nil, false, "before_id: unknown model id " + beforeID
		}
		end = i
	}
	if start > end {
		start = end
	}
	window := models[start:end]
	if beforeID != "" && afterID == "" {
		// Page immediately preceding the cursor.
		if len(window) > limit {
			hasMore = true
			window = window[len(window)-limit:]
		}
	} else if len(window) > limit {
		hasMore = true
		window = window[:limit]
	}
	return window, hasMore, ""
}

// lookupModel resolves a model id for GET /v1/models/{id}: exact catalog
// hit first, then the normalised id (provider prefix, date, -latest, [1m]
// stripped), then a synthesised entry for any Claude-family or Gemini id
// the POST path would accept. Garbage ids return ok=false (404).
func lookupModel(id string) (map[string]interface{}, bool) {
	all := allModels()
	find := func(want string) map[string]interface{} {
		for _, m := range all {
			if m["id"] == want {
				return m
			}
		}
		return nil
	}
	if m := find(id); m != nil {
		return decorateModel(m), true
	}
	norm := tiering.NormalizeModelID(id)
	if norm == "" || strings.ContainsAny(norm, " /\\") {
		return nil, false
	}
	if m := find(norm); m != nil {
		cp := decorateModel(m)
		cp["id"] = id
		return cp, true
	}
	fam := tiering.ModelFamily(norm)
	var base map[string]interface{}
	switch {
	case fam != "":
		base = find(fam)
	case strings.HasPrefix(norm, "gemini-"):
		base = find("gemini-2.5-flash")
	}
	if base == nil {
		return nil, false
	}
	cp := decorateModel(base)
	cp["id"] = id
	cp["display_name"] = prettyModelName(norm, fam)
	delete(cp, "description")
	return decorateModel(cp), true
}

// prettyModelName turns "claude-opus-4-5" into "Claude Opus 4.5".
func prettyModelName(norm, fam string) string {
	parts := strings.Split(norm, "-")
	var out []string
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if p == "" {
			continue
		}
		if _, err := strconv.Atoi(p); err == nil && i+1 < len(parts) {
			if _, err2 := strconv.Atoi(parts[i+1]); err2 == nil {
				out = append(out, p+"."+parts[i+1])
				i++
				continue
			}
		}
		out = append(out, strings.ToUpper(p[:1])+p[1:])
	}
	return strings.Join(out, " ")
}
