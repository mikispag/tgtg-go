package tgtg

import "fmt"

// LoginError reports a failed email or PIN authentication request.
type LoginError struct {
	// StatusCode is the HTTP response status.
	StatusCode int
	// Body is the unmodified response body.
	Body string
	// Err is the underlying decoding or validation error, if any.
	Err error
}

// Error returns the authentication failure and its underlying cause, if any.
func (e *LoginError) Error() string {
	message := fmt.Sprintf("tgtg login error: status %d: %s", e.StatusCode, e.Body)
	if e.Err != nil {
		return message + ": " + e.Err.Error()
	}
	return message
}

// Unwrap returns the underlying decoding or validation error.
func (e *LoginError) Unwrap() error { return e.Err }

// APIError reports an unsuccessful API response or an invalid response payload.
type APIError struct {
	// StatusCode is the HTTP response status, including for state failures.
	StatusCode int
	// State is the API's non-success order state, when present.
	State string
	// Body is the unmodified response body.
	Body string
	// Err is the underlying decoding or validation error, if any.
	Err error
}

// Error returns the API failure and its underlying cause, if any.
func (e *APIError) Error() string {
	var message string
	if e.State != "" {
		message = fmt.Sprintf("tgtg API error: state %s: %s", e.State, e.Body)
	} else {
		message = fmt.Sprintf("tgtg API error: status %d: %s", e.StatusCode, e.Body)
	}
	if e.Err != nil {
		return message + ": " + e.Err.Error()
	}
	return message
}

// Unwrap returns the underlying decoding or validation error.
func (e *APIError) Unwrap() error { return e.Err }

// PollingError reports that email authentication polling did not complete.
type PollingError struct {
	// Message describes why polling ended without credentials.
	Message string
}

// Error returns the polling failure message.
func (e *PollingError) Error() string { return e.Message }
