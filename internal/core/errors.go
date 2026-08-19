package core

import "errors"

// Typed errors for the entire Core package.
// Use errors.Is() to check; wrap with fmt.Errorf("%w: ...", err) to add context.

var (
	ErrOperationNotFound = errors.New("operation not found")
	ErrCapabilityMissing = errors.New("required capability missing")
	ErrInvalidPlan       = errors.New("invalid execution plan")
	ErrExecutionFailed   = errors.New("execution failed")
	ErrPermissionDenied  = errors.New("permission denied")
	ErrInvalidInput      = errors.New("invalid input")
)
