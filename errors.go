package tgtg

import "fmt"

type LoginError struct {
	StatusCode int
	Body       string
}

func (e *LoginError) Error() string {
	return fmt.Sprintf("tgtg login error: status %d: %s", e.StatusCode, e.Body)
}

type APIError struct {
	StatusCode int
	State      string
	Body       string
}

func (e *APIError) Error() string {
	if e.State != "" {
		return fmt.Sprintf("tgtg API error: state %s: %s", e.State, e.Body)
	}
	return fmt.Sprintf("tgtg API error: status %d: %s", e.StatusCode, e.Body)
}

type PollingError struct {
	Message string
}

func (e *PollingError) Error() string { return e.Message }
