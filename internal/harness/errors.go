package harness

import "errors"

// ErrInvalidConfig indicates a deployment configuration that the Harness cannot act on. It is a
// WIRING error only — never an execution / capability error (ADR-026 §3 errors.go scope).
var ErrInvalidConfig = errors.New("harness: invalid configuration")
