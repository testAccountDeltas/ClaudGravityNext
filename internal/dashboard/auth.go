// Part of the dashboard package's port from superrelay (v1)'s ui.go —
// see register.go for the package doc comment covering what's ported
// here vs. deferred, and page.go's doc comment for the dashboard.html
// embed. This file holds requireDashboardAuth, the HTTP Basic Auth gate
// in front of every dashboard-owned route except /api/bootstrap (see
// register.go for why that one is deliberately excluded).
package dashboard

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"claudgravity/internal/config"
)

// requireDashboardAuth gates the web UI (status page + its /api/* and
// /oauth/* management endpoints) behind HTTP Basic Auth. This is separate
// from the /v1/* Bearer-token tenant check in internal/httpapi — the
// dashboard exposes tenant keys, account emails and request logs.
// If the dashboard password is not configured (or whitespace-only), it
// fails closed with 401 Unauthorized to prevent open dashboard access.
func requireDashboardAuth(cfgMgr *config.ConfigManager, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want := ""
		if cfgMgr != nil {
			want = strings.TrimSpace(cfgMgr.DashboardPassword())
		}
		if want == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="ClaudGravity Dashboard"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		_, pass, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pass), []byte(want)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="ClaudGravity Dashboard"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}
