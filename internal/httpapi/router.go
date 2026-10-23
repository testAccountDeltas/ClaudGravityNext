// Part of the httpapi package — see errors.go for the package doc
// comment. NewRouter builds this package's http.Handler: mux wiring,
// recoverMiddleware, commonAPIMiddleware — a faithful port of the
// routing shape in v1's main() for the endpoints this package owns.
// Mounting this handler into a running process (TLS/CONNECT listener
// setup, graceful shutdown, startup account/model-catalog loading) is
// cmd/gateway/main.go's job, not this package's — see the package doc
// comment in errors.go for the full scope boundary.
package httpapi

import (
	"net/http"

	"claudgravity/internal/config"
)

// NewRouter registers every endpoint this package owns on a fresh mux
// and wraps it with the same middleware chain v1's main() used:
// recoverMiddleware(commonAPIMiddleware(mux)).
func NewRouter(cfgMgr *config.ConfigManager) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			handleMessages(cfgMgr, w, r)
		} else {
			methodNotAllowed(w, "POST")
		}
	})
	mux.HandleFunc("/v1/messages/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages/" {
			handleUnknownV1Path(w, r)
			return
		}
		if r.Method == http.MethodPost {
			handleMessages(cfgMgr, w, r)
		} else {
			methodNotAllowed(w, "POST")
		}
	})
	mux.HandleFunc("/v1/messages/cache_touch", handleCacheTouch)
	mux.HandleFunc("/v1/messages/cache_touch/", handleCacheTouch)
	mux.HandleFunc("/v1/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
		handleCountTokens(cfgMgr, w, r)
	})
	mux.HandleFunc("/v1/messages/count_tokens/", func(w http.ResponseWriter, r *http.Request) {
		handleCountTokens(cfgMgr, w, r)
	})
	mux.HandleFunc("/count_tokens", func(w http.ResponseWriter, r *http.Request) {
		handleCountTokens(cfgMgr, w, r)
	})
	mux.HandleFunc("/count_tokens/", func(w http.ResponseWriter, r *http.Request) {
		handleCountTokens(cfgMgr, w, r)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		handleModels(cfgMgr, w, r)
	})
	mux.HandleFunc("/v1/models/{id}", func(w http.ResponseWriter, r *http.Request) {
		handleModelByID(cfgMgr, w, r)
	})
	mux.HandleFunc("/v1/usage", func(w http.ResponseWriter, r *http.Request) {
		handleUsage(cfgMgr, w, r)
	})
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		handleEmbeddings(cfgMgr, w, r)
	})
	mux.HandleFunc("/v1/embeddings/", func(w http.ResponseWriter, r *http.Request) {
		handleEmbeddings(cfgMgr, w, r)
	})
	// /v1/complete (legacy text completions) and /v1/messages/batches are
	// unimplemented — answer clearly instead of falling through to
	// whatever catch-all handler cmd/gateway mounts alongside this
	// router (v1's dashboard demanded HTTP Basic Auth and returned an
	// HTML/text-plain 401 for these).
	mux.HandleFunc("/v1/complete", handleDeprecatedComplete)
	mux.HandleFunc("/v1/messages/batches", handleUnsupportedBatches)
	mux.HandleFunc("/v1/messages/batches/", handleUnsupportedBatches)
	mux.HandleFunc("/v1/messages/batches/{id}", handleUnsupportedBatches)
	mux.HandleFunc("/v1/messages/batches/{id}/results", handleUnsupportedBatches)
	mux.HandleFunc("/v1/messages/batches/{id}/cancel", handleUnsupportedBatches)
	// Registered last so Go's ServeMux only reaches it when nothing more
	// specific above matched — any other /v1/* path gets a clean
	// Anthropic 404 instead of a typo'd path silently falling through.
	mux.HandleFunc("/v1/", handleUnknownV1Path)

	return recoverMiddleware(CommonAPIMiddleware(mux))
}
