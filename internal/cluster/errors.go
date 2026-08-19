package cluster

import "errors"

// Coordination errors. These are metadata-layer errors (unknown cluster,
// missing member) — they never represent an execution failure, because Cluster
// performs no execution (ADR-016 MUST-1).
var (
	errClusterIDRequired = errors.New("cluster: cluster id required")
	errHostRefRequired   = errors.New("cluster: host ref required")
	errClusterNotFound   = errors.New("cluster: cluster not found")
	errMemberNotFound    = errors.New("cluster: member not found")
)

// Sentinel errors exposed for callers that want to wrap/compare.
var (
	// ErrClusterNotFound is returned by read/mutate ops on an unknown cluster.
	ErrClusterNotFound = errClusterNotFound
	// ErrMemberNotFound is returned by ops on a host that is not a member.
	ErrMemberNotFound = errMemberNotFound
)
