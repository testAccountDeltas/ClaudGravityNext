package main

import (
	"fmt"
	"net/http"
	"strings"

	"claudgravity/internal/config"
	"claudgravity/internal/httpapi"
	"claudgravity/internal/logx"
	"claudgravity/internal/mitm"
)

// newRootFallback builds the plain listener's "/" handler. Browser visits
// to "/" go to the dashboard. Any other unmatched path used to be forwarded
// to api.anthropic.com WITH the caller's credentials (tenant-key leak,
// audit P15); it now answers a 404 in Anthropic's error format unless the
// operator explicitly opted in with -allow-passthrough (and even then
// mitm.PassthroughToReal strips gateway secrets and, off an Anthropic
// Host, every credential header).
func newRootFallback(allowPassthrough bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		isAPI := mitm.IsAnthropicHost(r.Host) ||
			r.Header.Get("x-api-key") != "" ||
			strings.HasPrefix(r.Header.Get("Authorization"), "Bearer sk-") ||
			r.Header.Get("anthropic-version") != ""
		if r.URL.Path == "/" && !isAPI {
			http.Redirect(w, r, "/dashboard", http.StatusFound)
			return
		}
		if allowPassthrough {
			mitm.PassthroughToReal(w, r)
			return
		}
		logx.LogGateway("[ClaudGravityNext] 404 for unmapped %s %s (passthrough disabled)", r.Method, r.URL.Path)
		httpapi.WriteAnthropicError(w, http.StatusNotFound, "not_found_error",
			fmt.Sprintf("Not found: %s %s", r.Method, r.URL.Path))
	}
}

// installSecretMatcher teaches the passthrough which credentials belong to
// this gateway (tenant keys and the CONNECT proxy secret).
func installSecretMatcher(cfgMgr *config.ConfigManager) {
	mitm.SetSecretMatcher(func(v string) bool {
		if _, ok := cfgMgr.GetTenant(v); ok {
			return true
		}
		ps := cfgMgr.ProxySecret()
		return ps != "" && v == ps
	})
}
