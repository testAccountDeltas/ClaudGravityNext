// Part of the dashboard package's port from superrelay (v1)'s ui.go —
// see register.go for the package doc comment. This file holds the
// single static-asset dashboard page itself.
//
// v1 served dashboard.html via go:embed (a package-level
// `//go:embed dashboard.html` var in ui.go, read from disk once at
// compile time, not per-request) — replicated here unchanged. The file
// is copied byte-for-byte from superrelay/dashboard.html into this
// directory (see this package's dashboard.html).
package dashboard

import (
	_ "embed"
	"net/http"
)

//go:embed dashboard.html
var dashboardHTML string

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/dashboard" && r.URL.Path != "/dashboard/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(dashboardHTML))
}
