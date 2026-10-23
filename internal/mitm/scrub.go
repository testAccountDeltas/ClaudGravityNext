package mitm

import (
	"net/http"
	"strings"
	"sync/atomic"
)

// secretMatcher reports whether a credential string is one of this
// gateway's own secrets (a tenant API key or the CONNECT proxy secret).
// Such a value must never leave the process: PassthroughToReal forwards to
// the real Anthropic hosts, and a gateway key presented there is a leak.
var secretMatcher atomic.Value // func(string) bool

// SetSecretMatcher installs the predicate used by PassthroughToReal to
// recognise gateway-owned credentials. cmd/gateway wires it to the config
// manager (tenant keys + proxy secret).
func SetSecretMatcher(fn func(string) bool) {
	if fn == nil {
		fn = func(string) bool { return false }
	}
	secretMatcher.Store(fn)
}

func isGatewaySecret(v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	if fn, ok := secretMatcher.Load().(func(string) bool); ok && fn != nil {
		return fn(v)
	}
	return false
}

// scrubCredentialHeaders removes credentials from an outgoing passthrough
// request. Rules:
//   - never forward a gateway tenant key / proxy secret, in any header;
//   - when the request is NOT going to a genuine Anthropic host (the
//     destination was rewritten to api.anthropic.com for an unknown Host),
//     drop every credential header and cookie.
//
// A non-gateway credential addressed to a real Anthropic host (e.g. the
// user's own OAuth token during login flows) is left untouched.
func scrubCredentialHeaders(h http.Header, anthropicHost bool) {
	for _, name := range []string{"Authorization", "X-Api-Key", "Cookie", "Proxy-Authorization"} {
		vals := h.Values(name)
		if len(vals) == 0 {
			continue
		}
		if !anthropicHost || name == "Proxy-Authorization" {
			h.Del(name)
			continue
		}
		for _, v := range vals {
			tok := v
			if len(tok) > 7 && strings.EqualFold(tok[:7], "bearer ") {
				tok = tok[7:]
			}
			if isGatewaySecret(tok) || (name == "Cookie" && cookieHasSecret(v)) {
				h.Del(name)
				break
			}
		}
	}
}

func cookieHasSecret(cookie string) bool {
	for _, part := range strings.Split(cookie, ";") {
		if i := strings.IndexByte(part, '='); i >= 0 && isGatewaySecret(part[i+1:]) {
			return true
		}
	}
	return false
}
