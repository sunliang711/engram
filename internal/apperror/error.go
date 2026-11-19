package apperror

import (
	"errors"
	"fmt"
	"net/http"
)

// Code represents a standard Engram Server error code.
type Code string

const (
	CodeUnauthenticated       Code = "UNAUTHENTICATED"
	CodeUnauthorized          Code = "UNAUTHORIZED"
	CodeProjectExists         Code = "PROJECT_EXISTS"
	CodeProjectNotRegistered  Code = "PROJECT_NOT_REGISTERED"
	CodeProjectAmbiguous      Code = "PROJECT_AMBIGUOUS"
	CodeProjectContextInvalid Code = "PROJECT_CONTEXT_INVALID"
	CodeMemoryNotFound        Code = "MEMORY_NOT_FOUND"
	CodeVersionConflict       Code = "VERSION_CONFLICT"
	CodeDuplicateMemory       Code = "DUPLICATE_MEMORY"
	CodeIdempotencyConflict   Code = "IDEMPOTENCY_CONFLICT"
	CodeIdempotencyInProgress Code = "IDEMPOTENCY_IN_PROGRESS"
	CodeInvalidArgument       Code = "INVALID_ARGUMENT"
	CodeRateLimited           Code = "RATE_LIMITED"
	CodeInternalError         Code = "INTERNAL_ERROR"
)

// Error is the structured domain error returned across tools and HTTP responses.
type Error struct {
	Code      Code           `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	RequestID string         `json:"request_id,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	Cause     error          `json:"-"`
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (%v)", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	return e.Cause
}

// WithRequestID returns a shallow copy of Error with RequestID populated.
func (e *Error) WithRequestID(reqID string) *Error {
	cp := *e
	cp.RequestID = reqID
	return &cp
}

// WithDetails attaches structured metadata to the error.
func (e *Error) WithDetails(details map[string]any) *Error {
	cp := *e
	cp.Details = details
	return &cp
}

// New constructs a standard Error with default retryability for the given Code.
func New(code Code, message string) *Error {
	return &Error{
		Code:      code,
		Message:   message,
		Retryable: isRetryableByDefault(code),
	}
}

// Wrap constructs a standard Error wrapping an underlying error.
func Wrap(code Code, message string, err error) *Error {
	return &Error{
		Code:      code,
		Message:   message,
		Retryable: isRetryableByDefault(code),
		Cause:     err,
	}
}

// AsError converts any error into an *Error, defaulting to INTERNAL_ERROR.
func AsError(err error, reqID string) *Error {
	if err == nil {
		return nil
	}
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr.WithRequestID(reqID)
	}
	return &Error{
		Code:      CodeInternalError,
		Message:   "internal server error",
		Retryable: true,
		RequestID: reqID,
		Cause:     err,
	}
}

// HTTPStatus maps a standard error code to an HTTP status code when needed.
func (e *Error) HTTPStatus() int {
	switch e.Code {
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodeUnauthorized:
		return http.StatusForbidden
	case CodeProjectNotRegistered, CodeMemoryNotFound:
		return http.StatusNotFound
	case CodeProjectExists, CodeVersionConflict, CodeDuplicateMemory, CodeIdempotencyConflict, CodeIdempotencyInProgress, CodeProjectAmbiguous:
		return http.StatusConflict
	case CodeInvalidArgument, CodeProjectContextInvalid:
		return http.StatusBadRequest
	case CodeRateLimited:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

func isRetryableByDefault(code Code) bool {
	switch code {
	case CodeVersionConflict, CodeIdempotencyInProgress, CodeRateLimited, CodeInternalError:
		return true
	default:
		return false
	}
}
