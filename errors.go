package insight

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// APIError is a non-2xx response from the InsightRecorder API.
type APIError struct {
	// StatusCode is the HTTP status returned.
	StatusCode int
	// Message is the server's error string ({"error": "…"}), when present.
	Message string
	// RetryAfter carries the Retry-After header on 429 responses (0 if absent).
	RetryAfter time.Duration
	// Endpoint is the method and path that failed, for context.
	Endpoint string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	if e.Endpoint != "" {
		return fmt.Sprintf("insight: %s: %d %s", e.Endpoint, e.StatusCode, msg)
	}
	return fmt.Sprintf("insight: %d %s", e.StatusCode, msg)
}

// statusIs reports whether err is an *APIError with the given status.
func statusIs(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == status
}

// IsUnauthorized reports whether err was rejected for bad or missing
// credentials — usually a revoked API key or a wrong base URL.
func IsUnauthorized(err error) bool { return statusIs(err, http.StatusUnauthorized) }

// IsForbidden reports whether the credential lacks the required permission
// (e.g. an API key without the logs:write scope).
func IsForbidden(err error) bool { return statusIs(err, http.StatusForbidden) }

// IsNotFound reports whether the referenced resource does not exist — or
// belongs to another workspace, which is indistinguishable by design.
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsConflict reports whether the request collided with existing state (for
// example an Idempotency-Key reused with a different body).
func IsConflict(err error) bool { return statusIs(err, http.StatusConflict) }

// IsQuotaExceeded reports whether the workspace hit its plan's daily ingest
// quota. Retry after [RetryAfter]; retrying sooner will fail again.
func IsQuotaExceeded(err error) bool { return statusIs(err, http.StatusTooManyRequests) }

// IsUnavailable reports whether a dependency the request needed is not
// configured or is down (for example sending to a tracker with no integration).
func IsUnavailable(err error) bool { return statusIs(err, http.StatusServiceUnavailable) }

// RetryAfter returns how long the server asked the caller to wait, or 0.
func RetryAfter(err error) time.Duration {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.RetryAfter
	}
	return 0
}
