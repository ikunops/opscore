package execution

import "errors"

// ErrNotFound is returned when an execution id does not exist.
var ErrNotFound = errors.New("execution not found")

// ErrConflict is returned by Transition when the record is not currently in
// the expected 'from' state — a concurrent transition (e.g. a Cancel
// racing a Finish) already moved it. Callers must NOT force their own
// write; they react to the conflict instead. This is what makes status
// changes safe under concurrency (S3, Round 5).
var ErrConflict = errors.New("execution status transition conflict")

// Recorder is what the Executor writes lifecycle events into.
// The Executor depends ONLY on this interface — never on SQL directly —
// so Memory / SQLite / Postgres backends are interchangeable and the
// Kernel stays free of storage concerns.
type Recorder interface {
	Create(rec ExecutionRecord) error
	// UpdateStatus is a last-write-wins status set, retained for tests
	// and non-concurrent callers. New code should prefer Transition,
	// which is atomic and rejects races.
	UpdateStatus(id string, status Status) error
	// Transition atomically moves id from 'from' to 'to' only when the
	// record is currently in 'from' (and, for durable backends, its
	// version is unchanged). It returns ErrConflict when the status (or
	// version) no longer matches — so concurrent Cancel / Finish
	// cannot silently overwrite each other.
	Transition(id string, from, to Status) error
	UpdateStep(id string, step ExecutionStepRecord) error
}

// Store is the full persistence interface: Recorder plus reads.
// A single Store implementation can back both writing (Executor) and
// reading (the future Execution API / UI).
type Store interface {
	Recorder
	Get(id string) (*ExecutionRecord, error)
	List(q Query) ([]ExecutionRecord, error)
}
