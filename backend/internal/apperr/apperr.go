// Package apperr defines transport-agnostic application errors. Domain packages
// return them and the HTTP layer maps each Kind to an RFC 9457 problem response.
package apperr

import (
	"errors"
	"fmt"
)

// Kind classifies an application error.
type Kind int

const (
	// KindInternal is an unexpected failure (default for unknown errors).
	KindInternal Kind = iota
	// KindNotFound means the resource does not exist or is not visible.
	KindNotFound
	// KindInvalid means the input failed validation.
	KindInvalid
	// KindConflict means the request conflicts with current state.
	KindConflict
	// KindUnauthenticated means no valid identity was presented.
	KindUnauthenticated
	// KindForbidden means the caller is known but not allowed to do this.
	KindForbidden
	// KindMethodNotAllowed means the route exists but not for this HTTP method.
	KindMethodNotAllowed
	// KindUnavailable means a dependency needed to answer is down; the caller may retry.
	KindUnavailable
)

// Forbidden builds a KindForbidden error.
func Forbidden(message string) *Error {
	return &Error{Kind: KindForbidden, Code: "forbidden", Message: message}
}

// FieldError points at one invalid part of the input.
type FieldError struct {
	Pointer string `json:"pointer"` // RFC 6901 JSON pointer into the request body
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error is an application error with a stable machine-readable code.
type Error struct {
	Kind    Kind
	Code    string // e.g. "validation_failed", "stack_exists"
	Message string
	Fields  []FieldError
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// NotFound builds a KindNotFound error.
func NotFound(format string, args ...any) *Error {
	return &Error{Kind: KindNotFound, Code: "not_found", Message: fmt.Sprintf(format, args...)}
}

// Invalid builds a KindInvalid error with code and optional field errors.
func Invalid(code, message string, fields ...FieldError) *Error {
	return &Error{Kind: KindInvalid, Code: code, Message: message, Fields: fields}
}

// Conflict builds a KindConflict error.
func Conflict(code, message string) *Error {
	return &Error{Kind: KindConflict, Code: code, Message: message}
}

// As extracts an *Error from err's chain.
func As(err error) (*Error, bool) {
	var e *Error
	ok := errors.As(err, &e)
	return e, ok
}

// IsKind reports whether err wraps an *Error of kind k.
func IsKind(err error, k Kind) bool {
	e, ok := As(err)
	return ok && e.Kind == k
}
