// Package anthropicerr maps an HTTP status code to the Anthropic Messages
// API's own error "type" string. Extracted out of superrelay (v1)'s
// server.go rather than duplicated: internal/stream's chunk parser (an
// in-band Gemini error arriving mid-stream, mapped via its "code" field as
// a fallback) and the future internal/httpapi (writeAnthropicError, for a
// real HTTP-status failure) both need the identical mapping.
package anthropicerr

import "net/http"

// TypeForStatus returns the Anthropic error "type" Anthropic's own API
// would report for status.
func TypeForStatus(status int) string {
	switch status {
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusGatewayTimeout:
		// Documented as timeout_error (the request timed out while processing).
		return "timeout_error"
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return "overloaded_error"
	case http.StatusConflict:
		return "conflict_error"
	case http.StatusBadRequest:
		return "invalid_request_error"
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusPaymentRequired:
		// Anthropic's documented error type for exceeding an account's
		// spend/billing limit — the generic 4xx default
		// (invalid_request_error) gives a client no way to distinguish
		// "your request is malformed" from "you're out of credit."
		return "billing_error"
	case 529:
		return "overloaded_error"
	default:
		// Every client (4xx) error classifies as invalid_request_error;
		// api_error is reserved for the gateway's own 5xx failures.
		if status >= 400 && status < 500 {
			return "invalid_request_error"
		}
		return "api_error"
	}
}
