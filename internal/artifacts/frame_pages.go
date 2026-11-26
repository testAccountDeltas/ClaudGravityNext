package artifacts

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"time"
)

var slugRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type deployReq struct {
	Slug         string          `json:"slug"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	Content      string          `json:"content"`
	Contract     string          `json:"contract"`
	Capabilities json.RawMessage `json:"capabilities"`
}

func (h *Handler) deploy(w http.ResponseWriter, r *http.Request) {
	var req deployReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "malformed deploy body")
		return
	}
	if req.Slug != "" && !slugRe.MatchString(req.Slug) {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "invalid slug")
		return
	}
	caps := ""
	if len(req.Capabilities) > 0 && string(req.Capabilities) != "null" {
		caps = string(req.Capabilities)
	}
	a, err := h.store.Put(PutParams{Slug: req.Slug, Title: req.Title, Description: req.Description,
		Content: req.Content, Contract: req.Contract, Capabilities: caps})
	if err != nil {
		h.storeErr(w, err)
		return
	}
	resp := meta(a)
	resp["kind"] = "custom"
	resp["warnings"] = []string{}
	h.live.BroadcastVersion(a)
	writeJSON(w, http.StatusOK, resp)
}

// meta is the common slug/version/permissions block of read and deploy.
func meta(a Artifact) map[string]any {
	m := map[string]any{
		"slug":            a.Slug,
		"version":         a.VersionString(),
		"read":            "owner",
		"shared":          "none",
		"folder_audience": "owner",
		"contract":        ContractVersion,
	}
	if a.Capabilities != "" {
		m["capabilities"] = json.RawMessage(a.Capabilities)
	}
	return m
}

func (h *Handler) read(w http.ResponseWriter, slug string) {
	a, err := h.store.Get(slug)
	if err != nil {
		h.storeErr(w, err)
		return
	}
	m := meta(a)
	m["title"] = a.Title
	m["type"] = nil
	writeJSON(w, http.StatusOK, m)
}

func (h *Handler) content(w http.ResponseWriter, slug string) {
	a, err := h.store.Get(slug)
	if err != nil {
		h.storeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Without this marker the client treats the answer as "not served here"
	// and goes to the real *.frame.claudeusercontent.com host instead.
	w.Header().Set("x-frame-content-route", "served")
	_, _ = w.Write([]byte(a.Content))
}

func (h *Handler) frames(w http.ResponseWriter, r *http.Request) {
	limit := 25
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		limit = min(n, 200)
	}
	list := []Artifact{}
	// Everything stored here belongs to the single owner; nothing is "shared".
	if r.URL.Query().Get("rel") != "shared" {
		var err error
		if list, err = h.store.List(limit); err != nil {
			h.storeErr(w, err)
			return
		}
	}
	rows := make([]map[string]any, 0, len(list))
	for _, a := range list {
		rows = append(rows, map[string]any{
			"slug": a.Slug, "title": a.Title, "rel": "mine",
			"updatedAt": a.UpdatedAt.UTC().Format(time.RFC3339Nano),
			"starred":   a.Starred, "softDeleted": false,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"frames": rows, "starsEnabled": true, "next_page_token": nil})
}

func (h *Handler) remove(w http.ResponseWriter, slug string) {
	if err := h.store.Delete(slug); err != nil {
		h.storeErr(w, err)
		return
	}
	h.live.BroadcastDeleted(slug)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "slug": slug})
}

func (h *Handler) retitle(w http.ResponseWriter, r *http.Request, slug string) {
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Title == "" {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "title required")
		return
	}
	if err := h.store.Retitle(slug, req.Title); err != nil {
		h.storeErr(w, err)
		return
	}
	h.live.BroadcastTitle(slug, req.Title)
	writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "title": req.Title})
}

func (h *Handler) favorite(w http.ResponseWriter, slug string, on bool) {
	if err := h.store.SetStarred(slug, on); err != nil {
		h.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"starred": on})
}

func (h *Handler) versions(w http.ResponseWriter, slug string) {
	a, err := h.store.Get(slug)
	if err != nil {
		h.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": []map[string]any{{
		"version": a.VersionString(), "updatedAt": a.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}}})
}

func (h *Handler) contract(w http.ResponseWriter, parts []string) {
	if len(parts) == 1 || (len(parts) == 2 && (parts[1] == "latest" || parts[1] == ContractVersion)) {
		writeJSON(w, http.StatusOK, map[string]any{
			"version": ContractVersion, "capabilities": []string{"db"},
		})
		return
	}
	writeErr(w, http.StatusNotFound, "not_found_error", "unknown contract version")
}

// assetToken is what boot hands out for the follow-up /content request.
// It carries no authority: every /api/frame call already needs a tenant key.
const assetToken = "gateway"

// boot answers GET /api/frame/<slug>?via=model_read…, the first step of the
// client's `read`: the current version and the token for fetching the page.
func (h *Handler) boot(w http.ResponseWriter, slug string) {
	a, err := h.store.Get(slug)
	if err != nil {
		h.storeErr(w, err)
		return
	}
	m := meta(a)
	m["ver"] = a.VersionString()
	m["assetToken"] = assetToken
	m["title"] = a.Title
	// Share probe expects "owner" (not "private", which maps to unknown + shared-live)
	// and an explicit outsideWriter:false so single-owner artifacts are not treated as co-edited.
	m["mode"] = "owner"
	m["perm"] = map[string]any{"role": "owner", "mode": "owner"}
	m["outsideWriter"] = false
	m["outsideAudience"] = false
	m["cowritten"] = false
	m["authoredByOthers"] = false
	m["outsidersMayHaveWritten"] = false
	// "frame": fetch the page from /api/frame/content/<slug> on this same
	// host instead of the real *.frame.claudeusercontent.com content host.
	m["contentVia"] = "frame"
	h.attachSubscriptionToken(a.Slug, m)
	writeJSON(w, http.StatusOK, m)
}

func (h *Handler) subscribeFrame(w http.ResponseWriter, slug string, subscribed bool) {
	if !slugRe.MatchString(slug) {
		writeErr(w, http.StatusNotFound, "not_found_error", "artifact not found")
		return
	}
	if _, err := h.store.Get(slug); err != nil {
		h.storeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "slug": slug, "subscribed": subscribed})
}
