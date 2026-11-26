package artifacts

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"
)

var headOpenRe = regexp.MustCompile(`(?i)<head(?:\s[^>]*)?>`)

const claudeArtifactShim = `<script>(function(){try{window.localStorage.getItem("__p")}catch(_){var m=Object.create(null),s={getItem:function(k){return Object.prototype.hasOwnProperty.call(m,k)?m[k]:null},setItem:function(k,v){m[String(k)]=String(v)},removeItem:function(k){delete m[String(k)]},clear:function(){m=Object.create(null)},key:function(i){return Object.keys(m)[i]||null},get length(){return Object.keys(m).length}};try{Object.defineProperty(window,"localStorage",{configurable:true,get:function(){return s}})}catch(_){}}var c=window.claude=window.claude||{};c.user=c.user||{id:"local",name:"Owner"};if(!c.hot){var hotFns=[],hotReadyCalled=false;var fireHot=function(st){if(hotReadyCalled)return;hotReadyCalled=true;c.hot.state=st;for(var i=0;i<hotFns.length;i++){try{hotFns[i](st)}catch(_){}}};window.addEventListener("message",function(ev){if(ev&&ev.data&&ev.data.__cgHotInit!==undefined)fireHot(ev.data.__cgHotInit)});c.hot={ready:function(fn){if(typeof fn!=="function")return;hotFns.push(fn);if(hotReadyCalled){try{fn(c.hot.state)}catch(_){}return}if(document.readyState==="loading"){document.addEventListener("DOMContentLoaded",function(){setTimeout(function(){fireHot(c.hot.state)},15)})}else{setTimeout(function(){fireHot(c.hot.state)},15)}},snapshot:function(st){c.hot.state=st;try{window.parent.postMessage({__cgHotSnapshot:st},"*")}catch(_){}},register:function(fn){if(typeof fn==="function"){window.addEventListener("beforeunload",function(){try{c.hot.snapshot(fn())}catch(_){}})}},state:null}}if(!c.db){var db=Object.create(null);c.db={get:function(p,id){var col=db[p]||{};return Promise.resolve(Object.prototype.hasOwnProperty.call(col,id)?{exists:true,data:col[id]}:{exists:false})},list:function(p){var col=db[p]||{};return Promise.resolve({docs:Object.keys(col).map(function(k){return{id:k,data:col[k]}})})},query:function(p){return c.db.list(p)},set:function(p,id,d){db[p]=db[p]||{};db[p][id]=d;return Promise.resolve({version:1})},update:function(p,id,d){db[p]=db[p]||{};db[p][id]=Object.assign({},db[p][id]||{},d);return Promise.resolve({version:1})},delete:function(p,id){if(db[p])delete db[p][id];return Promise.resolve({version:1})}}}})();</script>`

// RegisterViewer mounts browser-facing viewer routes (/code/artifact/{slug}
// and /artifact/{slug}) without requiring a CLI bearer key.
func (h *Handler) RegisterViewer(mux *http.ServeMux) {
	mux.HandleFunc("/code/artifact/", h.serveViewer)
	mux.HandleFunc("/artifact/", h.serveViewer)
}

// RegisterDashboardAPI mounts admin list/delete endpoints for the dashboard's
// Artifacts tab, gated by the caller's dashboard auth wrapper.
func (h *Handler) RegisterDashboardAPI(mux *http.ServeMux, wrap func(http.HandlerFunc) http.HandlerFunc) {
	if wrap == nil {
		wrap = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}
	mux.HandleFunc("/api/dashboard/artifacts", wrap(h.serveDashboardList))
	mux.HandleFunc("/api/dashboard/artifacts/", wrap(h.serveDashboardItem))
}

func viewerRouteFromPath(path string) (slug, action string) {
	rest := path
	switch {
	case strings.HasPrefix(path, "/code/artifact/"):
		rest = strings.TrimPrefix(path, "/code/artifact/")
	case strings.HasPrefix(path, "/artifact/"):
		rest = strings.TrimPrefix(path, "/artifact/")
	default:
		return "", ""
	}
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", ""
	}
	parts := strings.Split(rest, "/")
	switch len(parts) {
	case 1:
		return parts[0], ""
	case 2:
		if parts[1] == "events" || parts[1] == "comments" {
			return parts[0], parts[1]
		}
	}
	return "", ""
}

func injectArtifactShim(doc string) string {
	if loc := headOpenRe.FindStringIndex(doc); loc != nil {
		return doc[:loc[1]] + claudeArtifactShim + doc[loc[1]:]
	}
	return claudeArtifactShim + doc
}

func (h *Handler) serveViewer(w http.ResponseWriter, r *http.Request) {
	slug, action := viewerRouteFromPath(r.URL.Path)
	if !slugRe.MatchString(slug) {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "events":
		h.serveViewerEvents(w, r, slug)
		return
	case "comments":
		h.serveViewerComments(w, r, slug)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a, err := h.store.Get(slug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "artifact store error", http.StatusInternalServerError)
		return
	}

	shimmedJSON, _ := json.Marshal(injectArtifactShim(a.Content))
	rawJSON, _ := json.Marshal(a.Content)
	slugJSON, _ := json.Marshal(a.Slug)
	basePath := "/code/artifact/" + a.Slug
	if strings.HasPrefix(r.URL.Path, "/artifact/") {
		basePath = "/artifact/" + a.Slug
	}
	basePathJSON, _ := json.Marshal(basePath)

	title := a.Title
	if title == "" {
		title = a.Slug
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = fmt.Fprintf(w, viewerPageHTML,
		html.EscapeString(title),
		html.EscapeString(title),
		html.EscapeString(a.VersionString()),
		html.EscapeString(a.Slug),
		string(shimmedJSON),
		string(rawJSON),
		string(slugJSON),
		string(basePathJSON),
	)
}

func (h *Handler) serveViewerEvents(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a, err := h.store.Get(slug)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "artifact store error", http.StatusInternalServerError)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	cli := &sseClient{
		send: make(chan sseEvent, sseSendQueueCap),
		done: make(chan struct{}),
	}
	h.live.registerSSE(slug, cli)
	defer func() {
		h.live.unregisterSSE(slug, cli)
		close(cli.done)
	}()

	title := a.Title
	if title == "" {
		title = a.Slug
	}
	threads, lerr := h.store.ListComments(slug)
	if lerr != nil || threads == nil {
		threads = []WireThread{}
	}
	initJSON, _ := json.Marshal(map[string]any{
		"slug":       a.Slug,
		"version":    a.VersionString(),
		"title":      title,
		"updated_at": a.UpdatedAt.UTC().Format(time.RFC3339),
		"active":     h.live.IsActive(slug),
		"threads":    threads,
	})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = fmt.Fprintf(w, "event: state\ndata: %s\n\n", initJSON)
	flusher.Flush()

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-cli.send:
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, ev.Data)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (h *Handler) serveViewerComments(w http.ResponseWriter, r *http.Request, slug string) {
	switch r.Method {
	case http.MethodGet:
		h.commentsList(w, slug)
	case http.MethodPost:
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		var req struct {
			Action    string `json:"action"`
			ThreadID  string `json:"thread_id"`
			Text      string `json:"text"`
			AskClaude *bool  `json:"ask_claude"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid_request_error", "malformed comment body")
			return
		}
		askClaude := true
		if req.AskClaude != nil {
			askClaude = *req.AskClaude
		}
		if req.Action == "resolve" {
			if !slugRe.MatchString(req.ThreadID) {
				writeErr(w, http.StatusBadRequest, "invalid_request_error", "valid thread_id required")
				return
			}
			h.commentResolve(w, slug, req.ThreadID, "")
			return
		}
		if strings.TrimSpace(req.Text) == "" {
			writeErr(w, http.StatusBadRequest, "invalid_request_error", "comment text required")
			return
		}
		if req.ThreadID != "" {
			if !slugRe.MatchString(req.ThreadID) {
				writeErr(w, http.StatusBadRequest, "invalid_request_error", "invalid thread_id")
				return
			}
			c, err := h.store.AddCommentReply(slug, req.ThreadID, req.Text, "", askClaude)
			if err != nil {
				h.storeErr(w, err)
				return
			}
			if threads, lerr := h.store.ListComments(slug); lerr == nil {
				h.live.BroadcastComment(slug, threads, askClaude)
			}
			writeJSON(w, http.StatusCreated, map[string]any{"thread_id": req.ThreadID, "comment": c})
			return
		}
		t, err := h.store.AddCommentThread(slug, req.Text, askClaude)
		if err != nil {
			h.storeErr(w, err)
			return
		}
		if threads, lerr := h.store.ListComments(slug); lerr == nil {
			h.live.BroadcastComment(slug, threads, askClaude)
		}
		writeJSON(w, http.StatusCreated, map[string]any{"thread": t})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) serveDashboardList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	list, err := h.store.List(200)
	if err != nil {
		h.storeErr(w, err)
		return
	}
	rows := make([]map[string]any, 0, len(list))
	for _, a := range list {
		rows = append(rows, map[string]any{
			"slug":        a.Slug,
			"title":       a.Title,
			"description": a.Description,
			"version":     a.Version,
			"starred":     a.Starred,
			"size_bytes":  len(a.Content),
			"updated_at":  a.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"artifacts": rows})
}

func (h *Handler) serveDashboardItem(w http.ResponseWriter, r *http.Request) {
	slug := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/dashboard/artifacts/"), "/")
	if !slugRe.MatchString(slug) {
		writeErr(w, http.StatusBadRequest, "invalid_request_error", "invalid artifact slug")
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := h.store.Delete(slug); err != nil {
		h.storeErr(w, err)
		return
	}
	h.live.BroadcastDeleted(slug)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "slug": slug})
}
