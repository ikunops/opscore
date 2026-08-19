package external

import "errors"

// ErrInvalidInput indicates a missing or malformed query argument (e.g. empty execution id / scope
// kind / scope ref). It is a contract/query error only — the External Interface defines no execution
// errors (MUST-0/3).
var ErrInvalidInput = errors.New("external: invalid input")

// ErrNotFound is returned by Get* methods when the requested entity does not exist. The HTTP layer
// maps it to HTTP 404 (R79-A). It is distinct from ErrInvalidInput (malformed/empty id, which is a
// 400-class contract error).
var ErrNotFound = errors.New("external: not found")
