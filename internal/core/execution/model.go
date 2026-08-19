// Package execution defines the lifecycle records and persistence
// abstractions for OpsCore's Execution Core (Phase 2.1.2).
//
// Design note: this package is intentionally decoupled from the core
// package — it depends only on the standard library. The core.Executor
// builds execution.ExecutionRecord values from plan/result primitives and
// writes them through the Recorder interface. This keeps the dependency
// direction one-way (core -> execution) and avoids an import cycle.
package execution

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Status is the lifecycle status of an Execution (one operation run).
type Status string

const (
	// StatusCreated is the record's initial state: allocated but not yet
	// submitted to planning. Used by ExecutionService.Submit before the async
	// goroutine takes over.
	StatusCreated Status = "CREATED"
	// StatusPlanning is set the moment an execution is submitted; the async
	// runner transitions it to Running once it actually starts stepping.
	StatusPlanning Status = "PLANNING"
	StatusRunning  Status = "RUNNING"
	// StatusCancelRequested is the explicit handshake state: a cancel was
	// requested (Running -> CancelRequested) but the run has not yet observed
	// ctx.Done() and stopped. Terminal Cancelled follows once it does.
	StatusCancelRequested Status = "CANCEL_REQUESTED"
	StatusCancelled       Status = "CANCELLED"
	StatusSuccess         Status = "SUCCESS"
	StatusFailed          Status = "FAILED"
)

// StepStatus is the status of a single step within an execution.
type StepStatus string

const (
	StepPending StepStatus = "PENDING"
	StepRunning StepStatus = "RUNNING"
	StepSuccess StepStatus = "SUCCESS"
	StepFailed  StepStatus = "FAILED"
)

// ExecutionRecord is the persisted lifecycle record of one operation run.
// It deliberately mirrors but does not reference core types, so the core
// package can depend on this package without a cycle.
type ExecutionRecord struct {
	ID         string
	Operation  string
	Permission string // ResourceType.Action
	Risk       string
	Status     Status

	UserID   string
	UserName string
	Target   string // empty => local; host addr => remote over SSH

	// TraceID is the operation's trace id (from core.Context). It travels with
	// the record so an execution is correlatable with its audit events and
	// (future) distributed traces without a join on the context.
	TraceID string

	// CapabilityHash is the integrity hash of the capability snapshot frozen
	// for this execution (Phase 2.8 / ADR-009). Weak reference only — the full
	// snapshot lives in a CapabilitySnapshotStore keyed by this hash, so an
	// execution can be replayed / audited against the exact capability view
	// without copying it into the hot execution table.
	CapabilityHash string

	// Source is the origin of the operation that produced this execution
	// ("builtin" | "system" | "plugin:<name>"). Carried on the record
	// (Phase 3.0 / MUST-1) so Audit and the Execution view know WHERE a
	// capability came from without a join back into the operations table.
	Source string

	// Origin is WHO triggered the execution (Phase 3.0 / SHOULD):
	// "API" | "CLI" | "BATCH" | "INTERNAL" | "PLUGIN". Persisted so
	// future audits can answer "who/what initiated this?" at a glance.
	Origin string

	// Version is an optimistic-concurrency token. Every state transition
	// stamps the version it observed and bumps it; a concurrent Cancel racing
	// an Executor update is detected instead of silently clobbering the row.
	Version uint64

	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	DurationMs int64

	Error string
	Steps []ExecutionStepRecord
}

// ExecutionStepRecord is one step within an ExecutionRecord.
// The stable ID lets the UI diff / retry / single-step audit later.
type ExecutionStepRecord struct {
	ID         string
	Name       string
	Index      int
	Status     StepStatus
	Success    bool
	Output     string
	Error      string
	DurationMs int64
}

// Query filters List results.
type Query struct {
	Operation string
	Status    Status
	Limit     int
	Offset    int
}

// NewExecutionID returns a unique execution identifier (prefix exe-).
func NewExecutionID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "exe-" + time.Now().Format("20060102150405.000")
	}
	return "exe-" + hex.EncodeToString(b)
}

// ExecutionEventType classifies a lifecycle event published on the EventBus.
type ExecutionEventType string

const (
	EventExecutionCreated     ExecutionEventType = "CREATED"
	EventExecutionStarted     ExecutionEventType = "STARTED"
	EventExecutionStepUpdated ExecutionEventType = "STEP_UPDATED"
	EventExecutionFinished    ExecutionEventType = "FINISHED"
	EventExecutionCancelled   ExecutionEventType = "CANCELLED"
)

// ExecutionEvent is emitted onto the EventBus as an execution moves through
// its lifecycle. Subscribers (Audit, WebSocket, future Metrics) consume it;
// the kernel never imports a concrete bus, only this interface — so the
// transport stays decoupled from core (no reverse dependency into the Kernel).
type ExecutionEvent struct {
	Type      ExecutionEventType
	ID        string
	Status    Status
	Timestamp time.Time
}

// EventBus is the minimal publish interface the ExecutionService depends on.
// A nil EventBus is a no-op, so the Kernel remains fully usable without any
// transport wired in (CLI, tests).
type EventBus interface {
	Publish(event ExecutionEvent)
}
