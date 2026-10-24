// Part of the requestlog package's port from superrelay (v1)'s
// request_logger.go — see store.go for the split rationale.
package requestlog

// MaskKey returns a display-safe form of a tenant API key: never the raw
// value, since the dashboard and the request_logs table both show this.
func MaskKey(key string) string {
	if len(key) <= 14 {
		// Short keys used to come back verbatim, leaking them whole into
		// the dashboard and the request log. Reveal at most two characters,
		// and none when the key is too short for that to be a small part.
		if len(key) < 8 {
			return "***"
		}
		return key[:2] + "..."
	}
	return key[:8] + "..." + key[len(key)-4:]
}
