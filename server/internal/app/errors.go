package app

import "fmt"

// Error codes (spec §14.2).
const (
	CodeInvalidArgument      = "INVALID_ARGUMENT"
	CodeUnauthenticated      = "UNAUTHENTICATED"
	CodePermissionDenied     = "PERMISSION_DENIED"
	CodeNotFound             = "NOT_FOUND"
	CodeConflict             = "CONFLICT"
	CodeFailedPrecondition   = "FAILED_PRECONDITION"
	CodeContentTooLarge      = "CONTENT_TOO_LARGE"
	CodeSecretDetected       = "SECRET_DETECTED"
	CodeUnsupportedVersion   = "UNSUPPORTED_VERSION"
	CodeUnsupportedOperation = "UNSUPPORTED_OPERATION"
	CodeInternal             = "INTERNAL"
)

// Error is an SCP error (spec §14.1). Its message never contains member-written text (§8.4).
type Error struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

func invalid(field, msg string) *Error {
	return &Error{Code: CodeInvalidArgument, Message: field + ": " + msg, Details: map[string]any{"field": field}}
}

var errNotFound = &Error{Code: CodeNotFound, Message: "not found"}
