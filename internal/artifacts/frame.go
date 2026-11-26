package artifacts

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"claudgravity/internal/logx"
)

// ContractVersion is the runtime contract this backend advertises.
const ContractVersion = "1.0.0"

const maxBody = 16 << 20

// Handler serves the Artifact backend routes.
type Handler struct {
	store     *Store
	authorize func(*http.Request) bool
	live      *LiveHub
}

// NewHandler returns a Handler. authorize decides whether a request may use
// the backend (nil allows everyone).
func NewHandler(store *Store, authorize func(*http.Request) bool) *Handler {
	return &Handler{store: store, authorize: authorize, live: NewLiveHub()}
}

// Register mounts the routes on mux, wrapping each with the given middlewares.
func (h *Handler) Register(mux *http.ServeMux, mw ...func(http.Handler) http.Handler) {
	wrap := func(next http.Handler) http.Handler {
		for i := len(mw) - 1; i >= 0; i-- {
			next = mw[i](next)
		}
		return next
	}
	mux.Handle("/api/frame/", wrap(http.HandlerFunc(h.serveFrame)))
	mux.Handle("/edge-api/frame-live/", wrap(http.HandlerFunc(h.serveLiveWS)))
	mux.Handle("/api/artifacts/mcp", wrap(http.HandlerFunc(h.serveMCP)))
	mux.Handle("/api/artifacts/mcp/", wrap(http.HandlerFunc(h.serveMCP)))
}

func (h *Handler) allowed(w http.ResponseWriter, r *http.Request) bool {
	if h.authorize == nil || h.authorize(r) {
		return true
	}
	writeErr(w, http.StatusUnauthorized, "authentication_error", "invalid or missing credentials")
	return false
}

func (h *Handler) serveFrame(w http.ResponseWriter, r *http.Request) {
	if !h.allowed(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/frame/"), "/")
	parts := strings.Split(rest, "/")
	head, arg := parts[0], ""
	if len(parts) > 1 {
		arg = parts[1]
	}
	m := r.Method
	switch {
	case head == "contract" && m == http.MethodGet:
		h.contract(w, parts)
	case head == "types" && m == http.MethodGet && len(parts) == 1:
		writeJSON(w, http.StatusOK, map[string]any{"types": []any{}, "next_page_token": nil})
	case head == "frames" && m == http.MethodGet:
		h.frames(w, r)
	case head == "read" && m == http.MethodGet && arg != "":
		h.read(w, arg)
	case head == "content" && m == http.MethodGet && arg != "":
		h.content(w, arg)
	case head == "deploy" && arg == "direct" && m == http.MethodPost:
		h.deploy(w, r)
	case head == "deploy" && arg == "prepare" && m == http.MethodPost:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "valid": true, "warnings": []string{}})
	case head == "retitle" && m == http.MethodPost && arg != "":
		h.retitle(w, r, arg)
	case head == "favorite" && arg != "" && (m == http.MethodPost || m == http.MethodDelete):
		h.favorite(w, arg, m == http.MethodPost)
	case head == "versions" && m == http.MethodGet && arg != "":
		h.versions(w, arg)
	case head == "db" && arg == "agent" && m == http.MethodPost:
		h.dbAgent(w, r)
	case head == "watch-token" && m == http.MethodPost && arg != "":
		h.watchToken(w, r, arg)
	case (head == "subscribe" || head == "unsubscribe") && m == http.MethodPost && arg != "":
		h.subscribeFrame(w, arg, head == "subscribe")
	case head == "comments" && arg != "" && len(parts) == 2 && m == http.MethodGet:
		h.commentsList(w, arg)
	case head == "comments" && arg != "" && len(parts) == 3 && m == http.MethodPost:
		h.commentReply(w, r, arg, parts[2])
	case head == "comments" && arg != "" && len(parts) == 4 && m == http.MethodPost && (parts[3] == "resolve" || parts[3] == "session-resolve"):
		h.commentResolve(w, arg, parts[2], "claude")
	case head == "comments" && arg != "" && len(parts) == 4 && m == http.MethodPost && parts[3] == "summon-status":
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case head == "track" && m == http.MethodPost:
		w.WriteHeader(http.StatusNoContent)
	case len(parts) == 1 && m == http.MethodGet && slugRe.MatchString(head):
		// The model's `read` first "boots" the artifact (JSON with ver and an
		// asset token); the page source is then fetched from /content/<slug>.
		h.boot(w, head)
	case len(parts) == 1 && m == http.MethodDelete:
		h.remove(w, head)
	default:
		logx.LogGateway("[artifacts] unhandled %s %s", m, r.URL.RequestURI())
		writeErr(w, http.StatusNotFound, "not_found_error", "Not found: "+m+" "+r.URL.Path)
	}
}

// serveMCP answers the hosted MCP endpoint with a clean 404. The client
// otherwise waits 30 s for it to connect and only then falls back to the
// built-in Frame API tool, which is the path this package implements.
func (h *Handler) serveMCP(w http.ResponseWriter, r *http.Request) {
	logx.LogGateway("[artifacts] MCP endpoint not provided: %s %s", r.Method, r.URL.Path)
	writeErr(w, http.StatusNotFound, "not_found_error", "artifacts MCP is not provided by this gateway")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, typ, msg string) {
	writeJSON(w, status, map[string]any{
		"type":  "error",
		"error": map[string]string{"type": typ, "message": msg},
	})
}

func (h *Handler) storeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not_found_error", "artifact not found")
		return
	}
	logx.LogGateway("[artifacts] store error: %v", err)
	writeErr(w, http.StatusInternalServerError, "api_error", "artifact store error")
}
