// Part of the mitm package's port from superrelay (v1)'s mitm.go — see
// ca.go for the split rationale and the frozen-behavior constraint.
package mitm

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

// ProxyAuthOK accepts the secret either as "Bearer <secret>" (manual
// testing, curl --proxy-header) or as HTTP Basic auth with the secret as
// the username (or password) (what HTTP clients send when HTTPS_PROXY is set to
// http://<secret>:x@host:port or http://:<secret>@host:port — the standard way to embed proxy
// credentials in the proxy URL). It fails closed if secret is empty.
func ProxyAuthOK(r *http.Request, secret string) bool {
	if strings.TrimSpace(secret) == "" {
		return false
	}
	header := r.Header.Get("Proxy-Authorization")
	if header == "" {
		header = r.Header.Get("Authorization")
	}
	lower := strings.ToLower(header)
	if strings.HasPrefix(lower, "bearer ") {
		bearer := strings.TrimSpace(header[7:])
		return subtle.ConstantTimeCompare([]byte(bearer), []byte(secret)) == 1
	}
	if strings.HasPrefix(lower, "basic ") {
		encoded := strings.TrimSpace(header[6:])
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return false
		}
		user := decoded
		pass := []byte{}
		if idx := bytes.IndexByte(decoded, ':'); idx >= 0 {
			user = decoded[:idx]
			pass = decoded[idx+1:]
		}
		if subtle.ConstantTimeCompare(user, []byte(secret)) == 1 {
			return true
		}
		if len(pass) > 0 && subtle.ConstantTimeCompare(pass, []byte(secret)) == 1 {
			return true
		}
	}
	return false
}

// proxyAuthOK is kept for backwards compatibility within the mitm package.
func proxyAuthOK(r *http.Request, secret string) bool {
	return ProxyAuthOK(r, secret)
}
