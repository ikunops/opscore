package core

import (
	"context"
	"sync"

	"github.com/YuDong999/opscore/internal/core/execution"
)

// CancelRegistry is the minimal interface the Executor uses to register its
// per-run cancel func, so an external Cancel(id) (e.g. from the HTTP API) can
// abort a running execution. The Executor is nil-safe: it only registers when
// a non-nil registry is attached, so the CLI / Phase 0 path is unaffected.
type CancelRegistry interface {
	Register(id string, cancel context.CancelFunc)
	Unregister(id string)
}

// cancelRegistry is the in-process implementation of CancelRegistry, keyed by
// execution id. It is concurrency-safe and also exposes Cancel(id) for the API.
type cancelRegistry struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newCancelRegistry() *cancelRegistry {
	return &cancelRegistry{cancels: make(map[string]context.CancelFunc)}
}

func (r *cancelRegistry) Register(id string, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels[id] = cancel
}

func (r *cancelRegistry) Unregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cancels, id)
}

// Cancel signals cancellation of a registered execution by id. It returns true
// if the id was known (a cancel func was signaled). Unknown ids are false.
func (r *cancelRegistry) Cancel(id string) bool {
	r.mu.Lock()
	cancel, ok := r.cancels[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// Runtime coordinates in-flight executions for a single process. It bundles the
// Executor with its Store (Recorder + reads), a CancelRegistry, and the
// ExecutionService facade so that runs are persisted (for the Execution API),
// cancellable by id, and async-submittable.
//
// The Kernel stays synchronous: the Executor itself never spawns goroutines.
// The async handshake (Planning -> Running -> terminal, plus the
// CancelRequested state) lives in the ExecutionService, keeping the
// per-run mutable state separate from the immutable ExecutionPlan.
type Runtime struct {
	executor *Executor
	store    execution.Store
	cancels  *cancelRegistry
	service  *ExecutionService
}

// NewRuntime wires an Executor to a durable Store and enables cancellation +
// the ExecutionService facade. It is the composition-root helper for serve
// mode. The same Store backend backs both the Executor's lifecycle records
// (a Store is also a Recorder) and the Execution API reads, so swapping
// MemoryStore for SQLite/Postgres later is a one-line change here.
func NewRuntime(executor *Executor, store execution.Store) *Runtime {
	reg := newCancelRegistry()
	executor.SetRecorder(store)
	executor.SetCancelRegistry(reg)
	svc := NewExecutionService(executor, store, reg, nil)
	return &Runtime{executor: executor, store: store, cancels: reg, service: svc}
}

// Run executes plan synchronously through the Executor and returns the
// execution record id (non-empty only when a Recorder is attached) alongside
// the result. The id is what callers pass to Cancel. (For the async
// 202-accepted flow, use Service().Submit instead.)
func (r *Runtime) Run(ctx Context, plan *ExecutionPlan) RunResult {
	// The synchronous / CLI entry point. Stamp the execution origin so the
	// record and Audit know it was triggered locally (Phase 3.0 / SHOULD).
	// Only when unset — an API-submitted plan already carries "API".
	if plan.Origin == "" {
		plan.Origin = "CLI"
	}
	id, res := r.executor.run(ctx, plan, "")
	return RunResult{ID: id, Result: res}
}

// Cancel requests cancellation of a running execution by id. It first marks the
// record CANCEL_REQUESTED (so the UI can show the in-flight request), then
// signals the run. Returns true if the id was known.
func (r *Runtime) Cancel(id string) bool {
	if r.store != nil {
		// CAS: only RUNNING -> CANCEL_REQUESTED. A finished/cancelled
		// record is left untouched (conflict = no-op).
		_ = r.store.Transition(id, execution.StatusRunning, execution.StatusCancelRequested)
	}
	return r.cancels.Cancel(id)
}

// Service exposes the async ExecutionService facade (Submit/Cancel/Get/List).
// The control plane sets its EventBus via Service().SetBus once a
// WebSocket hub is constructed.
func (r *Runtime) Service() *ExecutionService { return r.service }

// Store exposes the persistence backend for the Execution API (Get/List).
func (r *Runtime) Store() execution.Store { return r.store }

// RunResult couples an execution's record id with its result.
type RunResult struct {
	ID     string
	Result ExecutionResult
}
