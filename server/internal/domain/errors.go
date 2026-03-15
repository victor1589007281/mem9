package domain

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound      = errors.New("not found")
	ErrConflict      = errors.New("version conflict")
	ErrDuplicateKey  = errors.New("duplicate key")
	ErrValidation    = errors.New("validation error")
	ErrWriteConflict = errors.New("write conflict, retry")
)

// ValidationError wraps ErrValidation with a field-level message.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return e.Field + ": " + e.Message
	}
	return e.Message
}

func (e *ValidationError) Unwrap() error {
	return ErrValidation
}

// VersionConflictError is returned when If-Match version doesn't match.
// It carries the current memory so the client can merge and retry.
type VersionConflictError struct {
	Current         *Memory
	ExpectedVersion int
	ActualVersion   int
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version conflict: expected %d, actual %d", e.ExpectedVersion, e.ActualVersion)
}

func (e *VersionConflictError) Unwrap() error {
	return ErrConflict
}
