package anthropicerr

import "testing"

func TestTypeForStatus_Table(t *testing.T) {
	cases := map[int]string{
		400: "invalid_request_error",
		401: "authentication_error",
		402: "billing_error",
		403: "permission_error",
		404: "not_found_error",
		409: "invalid_request_error",
		413: "request_too_large",
		422: "invalid_request_error",
		429: "rate_limit_error",
		500: "api_error",
		502: "overloaded_error",
		503: "overloaded_error",
		504: "overloaded_error",
		529: "overloaded_error",
		599: "api_error",
		0:   "api_error",
		200: "api_error",
	}
	for status, want := range cases {
		if got := TypeForStatus(status); got != want {
			t.Errorf("TypeForStatus(%d)=%q want %q", status, got, want)
		}
	}
}
