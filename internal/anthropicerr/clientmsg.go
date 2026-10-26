package anthropicerr

import (
	"net/http"
	"strings"
)

const upstreamRetryPrefix = "upstream failed after retries:"

// ClientMessage turns an internal upstream failure string into a
// short message safe to show a client: wrapper prefixes (possibly doubled)
// are removed and anything that looks like raw Google JSON/RPC detail is
// replaced by a status-based sentence. The full text must be logged by the
// caller.
func ClientMessage(status int, errText string) string {
	msg := strings.TrimSpace(errText)
	for {
		l := strings.ToLower(msg)
		switch {
		case strings.HasPrefix(l, upstreamRetryPrefix):
			msg = strings.TrimSpace(msg[len(upstreamRetryPrefix):])
		case strings.HasPrefix(l, "upstream failed after retries"):
			msg = strings.TrimSpace(strings.TrimLeft(msg[len("upstream failed after retries"):], ": "))
		default:
			goto done
		}
	}
done:
	raw := strings.ContainsAny(msg, "{}[]") || strings.Contains(msg, "googleapis") ||
		strings.Contains(msg, "RESOURCE_EXHAUSTED") || strings.Contains(msg, "\n") || len(msg) > 300
	if msg != "" && !raw {
		return msg
	}
	switch {
	case status == http.StatusTooManyRequests:
		return "Rate limited by the upstream provider. Please retry later."
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return "The upstream provider rejected the request's credentials."
	case status >= 400 && status < 500:
		return "The upstream provider rejected the request."
	case status == 529 || status == http.StatusServiceUnavailable || status == http.StatusBadGateway || status == http.StatusGatewayTimeout:
		return "The upstream provider is temporarily unavailable. Please retry."
	}
	return "Upstream request failed. Please retry."
}
