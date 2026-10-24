package mitm

import (
	"encoding/base64"
	"net/http"
	"net/url"
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
		var keep []string
		for _, v := range vals {
			if !valueHasSecret(name, v) {
				keep = append(keep, v)
			}
		}
		if len(keep) == len(vals) {
			continue
		}
		h.Del(name)
		for _, v := range keep {
			h.Add(name, v)
		}
	}
}

// valueHasSecret reports whether a single header value carries a gateway
// secret in any supported encoding: raw, Bearer/Basic scheme (scheme is
// case-insensitive; Basic is base64 "user:pass" where either half may be
// the secret), or a cookie pair.
func valueHasSecret(name, v string) bool {
	v = strings.TrimSpace(v)
	if isGatewaySecret(v) {
		return true
	}
	if name == "Cookie" {
		return cookieHasSecret(v)
	}
	if sp := strings.IndexAny(v, " 	"); sp > 0 {
		scheme, rest := strings.ToLower(v[:sp]), strings.TrimSpace(v[sp+1:])
		switch scheme {
		case "bearer":
			return isGatewaySecret(rest)
		case "basic":
			dec, err := base64.StdEncoding.DecodeString(rest)
			if err != nil {
				dec, err = base64.RawStdEncoding.DecodeString(rest)
			}
			if err != nil {
				return false
			}
			if isGatewaySecret(string(dec)) {
				return true
			}
			user, pass, _ := strings.Cut(string(dec), ":")
			return isGatewaySecret(user) || isGatewaySecret(pass)
		}
	}
	return false
}

func cookieHasSecret(cookie string) bool {
	for _, part := range strings.Split(cookie, ";") {
		if i := strings.IndexByte(part, '='); i >= 0 && isGatewaySecret(part[i+1:]) {
			return true
		}
	}
	return false
}

// scrubQuery drops query parameters (any name) whose value is a gateway
// secret, e.g. ?key=<tenant key>. Returns the cleaned raw query.
func scrubQuery(raw string) string {
	if raw == "" {
		return raw
	}
	q, err := url.ParseQuery(raw)
	if err != nil {
		// Unparseable: drop it entirely if it mentions a secret.
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == '&' || r == ';' }) {
			if _, v, ok := strings.Cut(part, "="); ok && isGatewaySecret(v) {
				return ""
			}
		}
		return raw
	}
	changed := false
	for k, vs := range q {
		var keep []string
		for _, v := range vs {
			if isGatewaySecret(v) {
				changed = true
				continue
			}
			keep = append(keep, v)
		}
		if len(keep) == 0 {
			delete(q, k)
		} else {
			q[k] = keep
		}
	}
	if !changed {
		return raw
	}
	return q.Encode()
}
