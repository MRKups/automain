package domain

import (
	"errors"
	"fmt"
)

// ErrorCode classifies domain and operational failures for CLI and HTTP transports.
type ErrorCode string

const (
	ErrorCodeInvalidInput        ErrorCode = "invalid_input"
	ErrorCodeNotFound            ErrorCode = "not_found"
	ErrorCodeNotConfigured       ErrorCode = "not_configured"
	ErrorCodeExternalUnavailable ErrorCode = "external_unavailable"
	ErrorCodeInternal            ErrorCode = "internal_error"
)

// Error carries structured failure details and a recovery action across boundaries.
type Error struct {
	Code      ErrorCode `json:"code"`
	Operation string    `json:"operation"`
	Message   string    `json:"message"`
	Recovery  string    `json:"recovery,omitempty"`
	Err       error     `json:"-"`
}

// NewError constructs a typed domain Error.
func NewError(code ErrorCode, operation, message, recovery string, err error) *Error {
	return &Error{
		Code:      code,
		Operation: operation,
		Message:   message,
		Recovery:  recovery,
		Err:       err,
	}
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Recovery != "" {
		return fmt.Sprintf("%s (%s): %s Next action: %s", e.Operation, e.Code, e.Message, e.Recovery)
	}
	return fmt.Sprintf("%s (%s): %s", e.Operation, e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// AsError extracts a *domain.Error from err or wraps an unclassified error as ErrorCodeInternal.
func AsError(operation string, err error) *Error {
	if err == nil {
		return nil
	}
	var domErr *Error
	if errors.As(err, &domErr) {
		if domErr.Operation == "" && operation != "" {
			cp := *domErr
			cp.Operation = operation
			return &cp
		}
		return domErr
	}
	return &Error{
		Code:      ErrorCodeInternal,
		Operation: operation,
		Message:   err.Error(),
		Recovery:  "Run with --json or inspect stderr for the underlying failure cause.",
		Err:       err,
	}
}

// ExitCode maps a domain error to a deterministic process exit status.
// 0 = success, 1 = runtime/external/not_found failure, 2 = invalid input/usage, 3 = unconfigured adapter.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var domErr *Error
	if errors.As(err, &domErr) {
		switch domErr.Code {
		case ErrorCodeInvalidInput:
			return 2
		case ErrorCodeNotConfigured:
			return 3
		default:
			return 1
		}
	}
	return 1
}
