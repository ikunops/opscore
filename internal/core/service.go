package core

import (
	"time"

	"github.com/YuDong999/opscore/internal/core/execution"
)

// ExecutionService is the orchestration facade for Phase 2.1 (Execution
// = SSOT). It owns the async lifecycle that the kernel Executor
// deliberately does not:
//
//	Submit(ctx, plan)  -> Create(Planning) -> 202 + id
//	                     -> goroutine: UpdateStatus(Running) -> Executor.run
//	                        -> terminal status -> Publish(Finished)
//	Cancel(id, reason)  -> UpdateStatus(CancelRequested) -> registry.Cancel
//
// The Executor stays synchronous and single-path; the Service wraps it
// with the Planning/CancelRequested handshake and EventBus emission. The
// Service depends ONLY on the execution.EventBus *interface* — the concrete
// bus (WebSocket/Metrics) lives in the control plane, so the kernel is
// never reverse-poluted by a transport (Round 3 decision).
type ExecutionService struct {
	executor *Executor
	store    execution.Store
	cancels  *cancelRegistry
	bus      execution.EventBus // nil-safe: no-op when unset
}

// NewExecutionService builds the facade. bus may be nil (CLI/tests) — it
// is wired in by the control plane once a transport exists.
func NewExecutionService(executor *Executor, store execution.Store, cancels *cancelRegistry, bus execution.EventBus) *ExecutionService {
	return &ExecutionService{executor: executor, store: store, cancels: cancels, bus: bus}
}

// SetBus swaps the EventBus (idempotent). The control plane calls this
// once its WebSocket hub is constructed.
func (s *ExecutionService) SetBus(bus execution.EventBus) { s.bus = bus }

// Submit creates an ExecutionRecord at StatusPlanning, then runs it
// asynchronously: it returns immediately with the new id (HTTP responds
// 202 Accepted), and a goroutine drives the run to completion. The
// execution id is stamped onto the context so every AuditEvent the
// Executor emits for this run carries the correlation (kernel stays
// Audit-agnostic — it never imports the Service).
func (s *ExecutionService) Submit(ctx Context, plan *ExecutionPlan) (*execution.ExecutionRecord, error) {
	id := execution.NewExecutionID()
	rec := execution.ExecutionRecord{
		ID:             id,
		Operation:      plan.OperationName,
		Permission:     plan.Permission.String(),
		Risk:           plan.Risk.String(),
		Status:         execution.StatusPlanning,
		UserID:         ctx.User().ID,
		UserName:       ctx.User().Name,
		Target:         ctx.Target().Address,
		TraceID:        ctx.Trace().TraceID,
		CapabilityHash: capabilityHashOf(ctx),
		Source:         plan.Source,
		Origin:         plan.Origin,
		Version:        1,
		CreatedAt:      time.Now(),
	}
	if err := s.store.Create(rec); err != nil {
		return nil, err
	}
	s.publish(execution.ExecutionEvent{
		Type: execution.EventExecutionCreated, ID: id,
		Status: execution.StatusPlanning, Timestamp: time.Now(),
	})
	// Stamp the id so the Executor's audit events correlate.
	runCtx := ctx.WithExecutionID(id)
	go s.runAsync(runCtx, plan, id)
	return &rec, nil
}

// runAsync is the goroutine body: it owns the cancellable context for the
// run and registers its cancel func UP-FRONT, before the record is
// observable as Running. This closes the race where Service.Cancel(id)
// (called by a client after seeing Running) could land before the executor
// had registered its cancel func, leaving the run unable to observe the
// cancellation. After registration it flips to Running, executes via the
// kernel's runBody, then publishes a Finished event.
func (s *ExecutionService) runAsync(ctx Context, plan *ExecutionPlan, id string) {
	runCtx, cancel := WithCancel(ctx)
	s.cancels.Register(id, cancel)
	defer s.cancels.Unregister(id)

	// CAS: only flip PLANNING -> RUNNING. If a concurrent Cancel
	// already moved it (e.g. to CANCEL_REQUESTED) the transition
	// conflicts and we bail — the record owns its own terminal state.
	if err := s.store.Transition(id, execution.StatusPlanning, execution.StatusRunning); err != nil {
		return
	}
	s.publish(execution.ExecutionEvent{
		Type: execution.EventExecutionStarted, ID: id,
		Status: execution.StatusRunning, Timestamp: time.Now(),
	})
	_, res := s.executor.runBody(ctx, runCtx, plan, id, cancel, false)
	final := execution.StatusSuccess
	if !res.Success {
		if res.Cancelled {
			final = execution.StatusCancelled
		} else {
			final = execution.StatusFailed
		}
	}
	s.publish(execution.ExecutionEvent{
		Type: execution.EventExecutionFinished, ID: id,
		Status: final, Timestamp: time.Now(),
	})
}

// Cancel requests cancellation of a submitted execution. It first marks the
// record CancelRequested (so a UI can show the in-flight request), then
// signals the run via the cancel registry. Unknown ids are a no-op.
func (s *ExecutionService) Cancel(id, reason string) error {
	if s.store != nil {
		// CAS: only RUNNING -> CANCEL_REQUESTED. If the run already
		// finished (SUCCESS/FAILED) or was cancelled, this is a no-op
		// (conflict) and we must NOT clobber the terminal state.
		_ = s.store.Transition(id, execution.StatusRunning, execution.StatusCancelRequested)
	}
	if s.cancels != nil {
		s.cancels.Cancel(id)
	}
	return nil
}

// Get returns a single execution record by id.
func (s *ExecutionService) Get(id string) (*execution.ExecutionRecord, error) {
	return s.store.Get(id)
}

// List returns executions matching the query (filter by operation/status,
// paginate by limit/offset).
func (s *ExecutionService) List(q execution.Query) ([]execution.ExecutionRecord, error) {
	return s.store.List(q)
}

// publish emits a lifecycle event onto the (nil-safe) EventBus.
func (s *ExecutionService) publish(ev execution.ExecutionEvent) {
	if s.bus != nil {
		s.bus.Publish(ev)
	}
}
