// Part of the requestlog package's port from superrelay (v1)'s
// request_logger.go — see store.go for the split rationale.
package requestlog

// MaskKey returns a display-safe form of a tenant API key: never the raw
// value, since the dashboard and the request_logs table both show this.
func MaskKey(key string) string {
	if len(key) <= 14 {
		return key
	}
	return key[:8] + "..." + key[len(key)-4:]
}
