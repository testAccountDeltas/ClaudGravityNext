package dashboard

import (
	"net/http"
	"net/url"
	"strings"
)

// sameOriginOrNone guards the WebSocket upgrade against cross-site
// WebSocket hijacking: a browser attaches cached Basic credentials to a
// cross-site upgrade, so credentials alone are not proof of intent. A
// request is allowed when it carries no Origin header (non-browser client)
// or when the Origin's host equals the request Host.
func sameOriginOrNone(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}
