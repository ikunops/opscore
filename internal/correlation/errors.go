package correlation

import "errors"

// ErrInvalidScope is returned when a correlation query omits or mis-declares its Scope (SHOULD-5).
// Correlation never performs unbounded "correlate everything" joins; a valid Scope{Kind, Ref} is
// mandatory. Correlation also mints no new IDs, so a missing Ref is always an error (MUST-2).
var ErrInvalidScope = errors.New("correlation: invalid or missing scope (kind must be execution|plugin|host|policy and ref must be non-empty)")
