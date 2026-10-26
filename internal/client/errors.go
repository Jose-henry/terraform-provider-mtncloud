package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
)

// APIError is a non-2xx response from MTN Cloud. Every API failure the provider shows is one of
// these, so the wording and the "how to fix it" hints live here and nowhere else.
type APIError struct {
	StatusCode int
	Method     string
	Path       string // path only, never the query string (it can carry names the user typed)
	Message    string // the platform's own message, when it sent one
}

func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "MTN Cloud API %s %s: %d %s", e.Method, e.Path, e.StatusCode, http.StatusText(e.StatusCode))
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	}
	if hint := e.hint(); hint != "" {
		fmt.Fprintf(&b, ". %s", hint)
	}
	return b.String()
}

func (e *APIError) hint() string {
	switch e.StatusCode {
	case http.StatusUnauthorized:
		return "Check the provider credentials (MTNCLOUD_API_TOKEN, or MTNCLOUD_API_USERNAME, MTNCLOUD_API_PASSWORD and MTNCLOUD_API_TENANT)"
	case http.StatusForbidden:
		return "Your role does not allow this call. Ask your tenant administrator for the permission, or use a different account"
	case http.StatusTooManyRequests:
		return "The API is rate limiting requests. Lower -parallelism, or retry later"
	}
	return ""
}

// IsNotFound reports whether err is an API 404.
func IsNotFound(err error) bool { return hasStatus(err, http.StatusNotFound) }

// IsForbidden reports whether err is an API 403.
func IsForbidden(err error) bool { return hasStatus(err, http.StatusForbidden) }

// MayHaveSucceeded reports whether a failed write may still have been applied: the response
// was lost (a transport error) or the server or a gateway failed (5xx). Writes are never
// retried by the client, so callers check before writing again.
func MayHaveSucceeded(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	}
	return apiErr.StatusCode >= 500
}

// IsBadRequest reports whether err is an API 400.
func IsBadRequest(err error) bool { return hasStatus(err, http.StatusBadRequest) }

// IsConflict reports whether err is an API 409.
func IsConflict(err error) bool { return hasStatus(err, http.StatusConflict) }

func hasStatus(err error, code int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == code
}

// newAPIError builds an APIError from a response body. The platform usually answers
// {"success": false, "msg": "..."}, sometimes with an "errors" map of field messages.
func newAPIError(method, path string, status int, body []byte) *APIError {
	e := &APIError{StatusCode: status, Method: method, Path: path}
	var parsed struct {
		Msg    string         `json:"msg"`
		Errors map[string]any `json:"errors"`
	}
	if json.Unmarshal(body, &parsed) != nil {
		return e
	}
	parts := []string{}
	if parsed.Msg != "" {
		parts = append(parts, parsed.Msg)
	}
	fields := slices.Sorted(maps.Keys(parsed.Errors))
	for _, f := range fields {
		parts = append(parts, fmt.Sprintf("%s: %v", f, parsed.Errors[f]))
	}
	e.Message = strings.Join(parts, "; ")
	return e
}
