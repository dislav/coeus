package domain

import (
	"errors"
	"net/http"
)

// Sentinel domain errors. Wrap with fmt.Errorf("context: %w", ErrXxx) at I/O boundaries.
var (
	ErrNotFound       = NewError("not_found", "resource not found")
	ErrSessionExpired = NewError("session_expired", "session has expired")
	ErrDuplicate      = NewError("duplicate", "resource already exists")
	ErrValidation     = NewError("validation", "invalid input")
	ErrUnauthorized   = NewError("unauthorized", "authentication required")
	ErrForbidden      = NewError("forbidden", "insufficient role")
	ErrAIUnavailable  = NewError("ai_unavailable", "AI service unavailable")
	ErrSelfForbidden  = NewError("self_forbidden", "admin cannot change their own role or active state")
	ErrLastAdmin      = NewError("last_admin", "operation would remove the last active admin")
)

// Error is a typed domain error carrying a stable code for API responses.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func NewError(code, msg string) *Error { return &Error{Code: code, Message: msg} }

// HTTPStatus maps a domain error to its HTTP status code.
// Returns 500 for non-domain errors (unexpected).
func HTTPStatus(err error) int {
	var e *Error
	if !errors.As(err, &e) {
		return http.StatusInternalServerError
	}
	switch e.Code {
	case "not_found":
		return http.StatusNotFound
	case "session_expired":
		return http.StatusGone
	case "duplicate":
		return http.StatusConflict
	case "self_forbidden", "last_admin", "question_in_use":
		return http.StatusConflict
	case "validation":
		return http.StatusBadRequest
	case "unauthorized":
		return http.StatusUnauthorized
	case "forbidden":
		return http.StatusForbidden
	case "ai_unavailable":
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
