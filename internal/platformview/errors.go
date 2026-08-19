package platformview

import "errors"

// ErrMissingID is returned when a required existing ID (MUST-2) is empty. Platformview never mints
// a new ID; it only forwards IDs that already exist in the underlying capabilities.
var ErrMissingID = errors.New("platformview: required existing ID is empty")

// ErrPolicyNotFound is returned by GetGovernanceSummary when the requested policy ID does not exist
// in the underlying repository. It is the read-facade boundary's not-found signal; external/v1 maps
// it to HTTP 404 (R79-A).
var ErrPolicyNotFound = errors.New("platformview: policy not found")
