// Package protection implements Phase 21 Operational Protection: a peripheral
// admission layer that sits between the governance check and the executor.
//
// It is a GATE, not a DRIVER. It never executes, kills, or mutates plugins,
// runtime, policy, or execution state. It only admits or rejects a request and
// records the decision as a protection.* audit observation.
//
// Iron laws (ADR-048 / R95): R21-0..R21-14. All mechanically enforced by the
// property + mutation + AST-guard test suite in this package.
//
// Dependency discipline (R21-12, R93-④): this package defines its storage and
// audit interfaces (KillPersistence, AuditWriter, FailureEvidenceReader) but
// NEVER imports the storage or audit implementation packages. The wiring layer
// (controlplane) supplies concrete adapters, so the breaker depends only on the
// FailureEvidenceReader abstraction, not on storage/audit internals.
package protection

import (
	"context"
	"time"
)

// AuditWriter writes protection.* events. The protection package defines the
// interface; the controlplane wiring provides a storage-backed implementation.
// R93-④: these are observations, not Policy INTENT→CAS→OUTCOME mutations.
type AuditWriter interface {
	WriteEvent(ctx context.Context, event ProtectionEvent) error
}

// ProtectionEvent is a protection-specific audit observation (R93-④).
type ProtectionEvent struct {
	Timestamp time.Time
	Action    string // protection.* vocabulary (see Action* constants)
	CapID     string // capability / operation name
	Principal string // hashed principal, never cleartext
	Detail    string
}

// Protection action vocabulary (R93-④). Exactly seven reject actions; the audit
// stream can filter by `action LIKE 'protection.%'` to see protection pressure.
const (
	ActionKilled              = "protection.killed"
	ActionPrincipalKilled     = "protection.principal_killed"
	ActionCircuitOpen         = "protection.circuit_open"
	ActionBreakerUnknown      = "protection.breaker_unknown"
	ActionConcurrencyExceeded = "protection.concurrency_exceeded"
	ActionRateLimited         = "protection.rate_limited"
	ActionTimeout             = "protection.timeout"
)

// FailureEvidenceReader is the breaker's sole read dependency (R21-12). The
// breaker must depend ONLY on this interface, never on storage/audit directly.
// window is the sliding window over which recent failures are counted.
type FailureEvidenceReader interface {
	RecentFailures(capabilityID string, window time.Duration) (FailureWindow, error)
}

// FailureWindow is the breaker's view of recent execution failures. Truncated
// means the evidence source could not return a complete count — the visible
// Count is then a LOWER BOUND only (R21-8: fail closed on truncated evidence).
type FailureWindow struct {
	Count     int
	Truncated bool
}

// KillPersistence is the storage interface for protection kill-state (R93-③).
// The protection package defines it; storage/sqlite and storage/memory
// implement it. The in-memory map is only a runtime projection of persistent
// state. Single owner: only KillStore writes through this interface.
type KillPersistence interface {
	LoadKills() (map[string]bool, error)
	LoadPrincipalKills() (map[string]bool, error)
	SetKilled(capID string, killed bool) error
	SetPrincipalKilled(hash string, killed bool) error
	ListKills() ([]KillEntry, error)
}

// KillEntry is a single kill-state row for the read surface.
type KillEntry struct {
	CapabilityID  string
	Killed        bool
	KilledAt      time.Time
	KilledBy      string
	Principal     bool // true if this is a principal (not capability) kill row
	PrincipalHash string
}

// KillStoreState is the tri-state of the kill store (R21-13). Empty state must
// be distinguishable from failed state so the management read surface never
// reports a false-clean "no kills" when protection state is unavailable.
type KillStoreState int

const (
	// KillStateUninitialized means Bootstrap has not run (or has not completed).
	// Fail closed: every capability is treated as killed.
	KillStateUninitialized KillStoreState = iota
	// KillStateReady means Bootstrap loaded persistent state successfully.
	KillStateReady
	// KillStateFailed means Bootstrap failed to load persistent state. Fail
	// closed: every capability is treated as killed.
	KillStateFailed
)

func (k KillStoreState) String() string {
	switch k {
	case KillStateReady:
		return "ready"
	case KillStateFailed:
		return "failed"
	default:
		return "uninitialized"
	}
}

// Reject is a blocked request. Its shape intentionally mirrors a governance
// RBAC denial (R21-4): a normal HTTP error response.
type Reject struct {
	Action     string
	RetryAfter time.Duration
	HTTPStatus int
}

// Metrics holds the exact protection counters (R21-7, exact not windowed).
type Metrics struct {
	Admitted            int64
	Killed              int64
	PrincipalKilled     int64
	CircuitOpen         int64
	BreakerUnknown      int64
	ConcurrencyExceeded int64
	RateLimited         int64
	AuditWriteFailed    int64
}
