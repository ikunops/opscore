package core

import (
	"context"
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/core/execution"
)

// Executor runs an ExecutionPlan step by step.
// It maintains internal state (PlanRuntime concept) but does not expose it —
// Phase 0 is synchronous only. Async/Task-Engine comes in Phase 1+.
//
// Phase 2.1.3: Execute now derives a cancellable context per run and (when a
// CancelRegistry is attached) registers the run's cancel func under the
// execution record id. An external Cancel(id) — or a cancelled parent context
// (e.g. HTTP client disconnect) — aborts the run; the Executor observes
// ctx.Done() between steps and marks the record CANCELLED.
type Executor struct {
	audit     AuditSink
	recorder  execution.Recorder // optional; nil => no execution lifecycle persisted
	cancelReg CancelRegistry     // optional; nil => runs are not externally cancellable
}

func NewExecutor(audit AuditSink) *Executor {
	return &Executor{audit: audit}
}

// SetRecorder attaches an ExecutionStore recorder. When nil (the default),
// the execution lifecycle is not persisted and behaviour is unchanged from
// Phase 0. Call at most once at the composition root (main / serve).
func (e *Executor) SetRecorder(r execution.Recorder) { e.recorder = r }

// SetCancelRegistry attaches a CancelRegistry so an external Cancel(id) can
// abort a running execution. Nil-safe: when unset, runs are not cancellable.
// Called once at the composition root (via NewRuntime).
func (e *Executor) SetCancelRegistry(r CancelRegistry) { e.cancelReg = r }

// Execute runs all steps in the plan sequentially. It is the public,
// synchronous API used by the Dispatcher (Phase 0). Cancellation is honored
// internally but the call blocks until the run completes (success, failure,
// or cancellation).
func (e *Executor) Execute(ctx Context, plan *ExecutionPlan) ExecutionResult {
	_, res := e.run(ctx, plan, "")
	return res
}

// run is the synchronous executor entry point (Phase 0 / Runtime.Run). It
// derives a cancellable context for the run and — when a CancelRegistry is
// attached and this is a known execution id — registers its cancel func so an
// external Cancel(id) can abort it. It then delegates the actual stepping to
// runBody.
//
// The async ExecutionService path must register the cancel func BEFORE the run
// is observable as Running (see runAsync), so it builds the context and
// registers itself, then calls runBody directly — runBody never creates or
// registers a context of its own. This keeps the kernel single-path.
func (e *Executor) run(ctx Context, plan *ExecutionPlan, recID string) (string, ExecutionResult) {
	runCtx, cancel := WithCancel(ctx)
	defer cancel()
	// Sync/ad-hoc path: the Executor allocates the record (recID is "") and
	// owns the cancel-registration for it. The async path passes a non-empty
	// id and registers up-front in runAsync, so it calls runBody directly
	// with ownsCancel=false.
	return e.runBody(ctx, runCtx, plan, recID, cancel, true)
}

// runBody executes the plan. ctx is the PARENT context — the capability
// source and the audit principal — while runCtx is the cancellable child the
// steps actually run against, so a cancellation only affects this run.
// capHash is frozen from ctx at entry (WithCancel gives the child a fresh,
// empty snapshot cache by design, so the snapshot must be read from the parent).
//
// recID selects the lifecycle owner:
//   - ""        -> the run allocates a fresh record (synchronous Dispatcher
//     path) and creates it directly at StatusRunning;
//   - non-empty -> the record was pre-created (at StatusPlanning) by
//     ExecutionService.Submit; the run uses it as-is and only
//     flips it to Running, then to a terminal state. The async
//     handshake (Planning -> Running -> terminal) lives in the
//     Service, not here, so the kernel stays single-path.
//
// cancel is the cancel func of runCtx; when ownsCancel is true the Executor
// owns its registration (the sync path, where recID is allocated inside). The
// async caller registers id up-front (before the record is Running-visible)
// and passes ownsCancel=false so runBody does not double-register — a second
// defer would otherwise tear the async registration out from under it.
func (e *Executor) runBody(ctx, runCtx Context, plan *ExecutionPlan, recID string, cancel context.CancelFunc, ownsCancel bool) (string, ExecutionResult) {
	start := time.Now()
	// Frozen at entry: the capability snapshot in effect for this target.
	// Read from the parent ctx (not the WithCancel child below).
	capHash := capabilityHashOf(ctx)

	// Internal execution state — not exposed (PlanRuntime in GPT's model)
	result := ExecutionResult{
		Steps: make([]StepResult, 0, len(plan.Steps)),
	}

	if e.recorder != nil {
		if recID == "" {
			// Synchronous / ad-hoc path: allocate and create the record
			// directly at Running. The async ExecutionService path passes
			// a pre-created Planning record and flips it to Running itself.
			recID = execution.NewExecutionID()
			_ = e.recorder.Create(execution.ExecutionRecord{
				ID:             recID,
				Operation:      plan.OperationName,
				Permission:     plan.Permission.String(),
				Risk:           plan.Risk.String(),
				Status:         execution.StatusRunning,
				UserID:         ctx.User().ID,
				UserName:       ctx.User().Name,
				Target:         ctx.Target().Address,
				CapabilityHash: capHash,
				Source:         plan.Source,
				Origin:         plan.Origin,
				CreatedAt:      start,
			})
		}
	}
	// Register the cancel func so an external Cancel(id) aborts this run.
	// Only on the owning (sync) path — the async caller already registered
	// id up-front and would have its registration torn down by our defer if
	// we re-registered here.
	if ownsCancel && e.cancelReg != nil && recID != "" {
		e.cancelReg.Register(recID, cancel)
		defer e.cancelReg.Unregister(recID)
	}

	// Per-plan timeout (in addition to per-step timeouts)
	if plan.Timeout > 0 {
		// Phase 0/2.1: we rely on per-step timeouts. Plan-level timeout uses
		// the cancellable context above in a later sub-phase.
		ctx.Logger().Debug("plan_timeout_set", "timeout", plan.Timeout)
	}

	for i, step := range plan.Steps {
		// Cancellation requested before this step -> stop cleanly as CANCELLED.
		if runCtx.Err() != nil {
			return e.cancelRun(recID, plan, result, start, runCtx, capHash)
		}

		ctx.Logger().Debug("executing_step",
			"plan", plan.OperationName,
			"step_index", i,
			"step", step.Describe(),
		)

		stepResult := step.Execute(runCtx)
		result.Steps = append(result.Steps, stepResult)
		result.Output += stepResult.Output

		if e.recorder != nil && recID != "" {
			_ = e.recorder.UpdateStep(recID, execution.ExecutionStepRecord{
				ID:         stepResult.StepID,
				Name:       stepResult.StepName,
				Index:      stepResult.Index,
				Status:     stepStatus(stepResult.Success),
				Success:    stepResult.Success,
				Output:     stepResult.Output,
				Error:      errText(stepResult.Error),
				DurationMs: stepResult.Duration.Milliseconds(),
			})
		}

		// Cancelled during/after the step -> the run is CANCELLED, not FAILED.
		if runCtx.Err() != nil {
			return e.cancelRun(recID, plan, result, start, runCtx, capHash)
		}

		if !stepResult.Success {
			result.Success = false
			result.Error = fmt.Errorf("%w: step %q failed: %v",
				ErrExecutionFailed, stepResult.StepName, stepResult.Error)
			result.Duration = time.Since(start)

			e.finishRecord(recID, result)
			e.emitAudit(ctx, plan, result, start, capHash)
			return recID, result
		}
	}

	result.Success = true
	result.Duration = time.Since(start)

	e.finishRecord(recID, result)
	e.emitAudit(runCtx, plan, result, start, capHash)
	return recID, result
}

// cancelRun finalizes a run aborted by cancellation.
func (e *Executor) cancelRun(recID string, plan *ExecutionPlan, result ExecutionResult, start time.Time, runCtx Context, capHash string) (string, ExecutionResult) {
	result.Success = false
	result.Cancelled = true
	if runCtx.Err() != nil {
		result.Error = runCtx.Err() // typically context.Canceled
	} else {
		result.Error = fmt.Errorf("execution cancelled")
	}
	result.Duration = time.Since(start)

	e.finishRecordStatus(recID, execution.StatusCancelled)
	e.emitAudit(runCtx, plan, result, start, capHash)
	return recID, result
}

// finishRecord writes SUCCESS/FAILED based on the result (no-op when nil).
func (e *Executor) finishRecord(recID string, result ExecutionResult) {
	final := execution.StatusSuccess
	if !result.Success {
		final = execution.StatusFailed
	}
	e.finishRecordStatus(recID, final)
}

// finishRecordStatus writes an explicit terminal status via CAS
// (RUNNING -> status). If a concurrent Cancel already moved the record
// (to CANCEL_REQUESTED), the primary transition conflicts, and we settle
// it to CANCELLED so it never lingers in a requested-but-not-terminal
// state — the cancel request is authoritative.
func (e *Executor) finishRecordStatus(recID string, status execution.Status) {
	if e.recorder == nil || recID == "" {
		return
	}
	if err := e.recorder.Transition(recID, execution.StatusRunning, status); err == nil {
		return
	}
	_ = e.recorder.Transition(recID, execution.StatusCancelRequested, execution.StatusCancelled)
}

// capabilityHashOf returns the integrity hash of the capability snapshot in
// effect for the context's current target, or "" when none has been observed.
// Phase 2.8 (ADR-009): the Executor records this on the ExecutionRecord and
// AuditEvent so each run is traceable to the exact capabilities that drove it.
// It is a weak reference — the full snapshot lives in a CapabilitySnapshotStore
// keyed by this hash, keeping the execution/audit tables lean.
func capabilityHashOf(ctx Context) string {
	if snap := ctx.CapabilitySnapshot(); snap != nil {
		return snap.Hash()
	}
	return ""
}

func stepStatus(ok bool) execution.StepStatus {
	if ok {
		return execution.StepSuccess
	}
	return execution.StepFailed
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (e *Executor) emitAudit(ctx Context, plan *ExecutionPlan, result ExecutionResult, start time.Time, capHash string) {
	if e.audit == nil {
		return
	}
	e.audit.Emit(AuditEvent{
		TraceID:        ctx.Trace().TraceID,
		Timestamp:      time.Now(),
		OperationName:  plan.OperationName,
		User:           ctx.User(),
		Host:           ctx.Host(),
		Target:         ctx.Target().Address,
		CapabilityHash: capHash,
		ExecutionID:    ctx.ExecutionID(),
		Permission:     plan.Permission,
		Risk:           plan.Risk,
		Result:         result,
		Duration:       time.Since(start),
	})
}
