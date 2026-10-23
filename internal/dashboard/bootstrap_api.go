// Part of the dashboard package's port from superrelay (v1)'s ui.go —
// see register.go for the package doc comment. This file holds
// handleAPIBootstrap, the one dashboard-owned endpoint deliberately NOT
// behind requireDashboardAuth — see register.go for why.
package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"

	"claudgravity/internal/config"
	"claudgravity/internal/mitm"
)

// handleAPIBootstrap hands the installer scripts everything they need to
// configure a Claude Code client against this gateway, given nothing but
// the gateway's own base URL and a valid tenant key: the proxy secret
// (CONNECT-proxy auth), the MITM CA in PEM form (so the client can trust
// the TLS API port), and which host/ports to actually point at. Without
// this, distributing a gateway meant anyone had to be handed the CA file
// and proxy_secret out of band in addition to their API key.
func handleAPIBootstrap(cfgMgr *config.ConfigManager, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	key := r.URL.Query().Get("key")
	if key == "" {
		if auth := r.Header.Get("Authorization"); auth != "" {
			key = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if key == "" {
		http.Error(w, `{"error":"missing ?key=<your tenant API key>"}`, http.StatusUnauthorized)
		return
	}
	if _, ok := cfgMgr.GetTenant(key); !ok {
		http.Error(w, `{"error":"invalid or unknown API key"}`, http.StatusUnauthorized)
		return
	}

	serverCfg := cfgMgr.GetServerConfig()
	caPEM := ""
	if mitm.GlobalMitmCA != nil {
		caPEM = mitm.GlobalMitmCA.GetCAPEM()
	}

	// GAP-O2: this used to report serverCfg.Port — the plain API port —
	// instead of the actual CONNECT-tunnel proxy port, which a client
	// must point its HTTPS_PROXY/proxy settings at. cfgMgr.ConnectPort()
	// is the value cmd/gateway/main.go resolved and recorded at startup
	// (its own -connect-port flag, defaulting to Server.Port+1).
	connectPort := cfgMgr.ConnectPort()
	if connectPort == 0 {
		// Only possible before main.go's own startup sequence reaches
		// SetConnectPort, or if the CONNECT listener never got configured
		// at all — fall back to the documented default rather than
		// reporting a bare 0 an installer would try to connect to.
		connectPort = serverCfg.Port + 1
	}
	resp := map[string]interface{}{
		"public_host":  serverCfg.PublicHost,
		"connect_port": connectPort,
		"tls_port":     serverCfg.TLSPort,
		"proxy_secret": serverCfg.ProxySecret,
		"ca_pem":       caPEM,
	}
	_ = json.NewEncoder(w).Encode(resp)
}
