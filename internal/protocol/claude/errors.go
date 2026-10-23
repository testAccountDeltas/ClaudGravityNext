// Part of the claude protocol package — see message.go for the package
// doc comment. ErrorType's values are grounded in the exact strings
// superrelay (v1)'s server.go's anthropicErrorTypeForStatus maps HTTP
// statuses onto.
package claude

type ErrorType string

const (
	ErrorInvalidRequest  ErrorType = "invalid_request_error"
	ErrorAuthentication  ErrorType = "authentication_error"
	ErrorPermission      ErrorType = "permission_error"
	ErrorNotFound        ErrorType = "not_found_error"
	ErrorRateLimit       ErrorType = "rate_limit_error"
	ErrorAPI             ErrorType = "api_error"
	ErrorOverloaded      ErrorType = "overloaded_error"
	ErrorRequestTooLarge ErrorType = "request_too_large"
	ErrorBilling         ErrorType = "billing_error"
)

type ErrorDetail struct {
	Type    ErrorType `json:"type"`
	Message string    `json:"message"`
}

// ErrorEnvelope is the body of a non-2xx /v1/messages response, and also
// the payload of an "error" SSE event sent mid-stream.
type ErrorEnvelope struct {
	Type  string      `json:"type"` // "error"
	Error ErrorDetail `json:"error"`
}
