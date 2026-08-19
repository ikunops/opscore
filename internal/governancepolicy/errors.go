package governancepolicy

import "errors"

// Sentinel errors for the policy repository / lifecycle (ADR-030 SHOULD-3:
// explainable, stable error contract).
var (
	// ErrNotFound is returned when a PolicyID does not exist in the repository.
	ErrNotFound = errors.New("governancepolicy: policy not found")
	// ErrInvalidID is returned when a PolicyID is empty or unsafe for storage
	// (e.g. would allow path traversal out of the store directory).
	ErrInvalidID = errors.New("governancepolicy: invalid policy id")
	// ErrConflict is reserved for a revision race (next revision already taken).
	//
	// NOT the Phase 17 CAS sentinel. It predates the Management API and has no
	// callers; new code must use ErrRevisionConflict. Retained rather than
	// deleted because removing it would be a Phase 14 edit (ADR-036 §4).
	ErrConflict = errors.New("governancepolicy: revision conflict")

	// ErrRevisionConflict is the Phase 17 compare-and-swap failure (ADR-036
	// §3.2.1 CAS-3, §3.2.2 CT-2). The caller's expected revision does not match
	// the stored one — including the "must not already exist" case
	// (expectedRevision == 0 against an existing policy, CAS-1). The Management
	// surface maps it to 409.
	//
	// Deliberately distinct from ErrIllegalTransition: a revision conflict says
	// "your view of the record is stale"; a transition error says "your view is
	// current, but the move is not allowed". CT-9 fixes the precedence — the
	// revision is always compared first.
	ErrRevisionConflict = errors.New("governancepolicy: revision conflict (compare-and-swap)")

	// ErrIllegalTransition is returned by CompareAndTransition when the revision
	// matched but the requested lifecycle move is not admitted by the state
	// machine (ADR-036 §3.2.2 / §4): Draft <-> Active, Draft|Active -> Archived,
	// Archived terminal. A self-transition is NOT illegal — it is a no-op
	// success (CT-8).
	ErrIllegalTransition = errors.New("governancepolicy: illegal lifecycle transition")
)
