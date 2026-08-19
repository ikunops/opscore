package core

import "fmt"

// Dispatcher routes operation names to their Handlers.
// It exposes two methods: Plan (dry-run) and Execute (real run).
//
// Dispatcher knows only the Handler interface — it does not distinguish
// between builtin handlers and plugin handlers. This eliminates the
// "if plugin" permanent branch.
type Dispatcher struct {
	registry *Registry
	executor *Executor
}

func NewDispatcher(registry *Registry, executor *Executor) *Dispatcher {
	return &Dispatcher{registry: registry, executor: executor}
}

// Plan builds an ExecutionPlan without executing it.
// This is the DryRun API — GPT: "Dispatcher.Plan() is DryRun".
func (d *Dispatcher) Plan(ctx Context, opName string, input map[string]any) (*ExecutionPlan, error) {
	op, ok := d.registry.Get(opName)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrOperationNotFound, opName)
	}

	plan, err := op.Handler.Plan(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("plan failed for %s: %w", opName, err)
	}

	// Enforce defaults from the Operation registration
	if plan.OperationName == "" {
		plan.OperationName = op.Name
	}
	if plan.Permission.ResourceType == "" {
		plan.Permission = op.Permission
	}
	// Carry the registration origin so the Executor can stamp it onto the
	// ExecutionRecord (Phase 3.0 / MUST-1) without a Storage join.
	plan.Source = op.Source

	return plan, nil
}

// Execute plans and then runs the operation.
func (d *Dispatcher) Execute(ctx Context, opName string, input map[string]any) ExecutionResult {
	plan, err := d.Plan(ctx, opName, input)
	if err != nil {
		return ExecutionResult{Success: false, Error: err}
	}

	return d.executor.Execute(ctx, plan)
}

// ListOperations returns all registered operations.
func (d *Dispatcher) ListOperations() []Operation {
	return d.registry.List()
}

// BatchResult is the outcome of a single target within a batch fan-out.
type BatchResult struct {
	Target  TargetHost
	Success bool
	Error   string
	Result  ExecutionResult
}

// Batch fans out one operation across multiple targets (Phase 2.5). For each
// target it derives a child Context carrying that target (WithTarget) and
// executes the operation independently. Failures are isolated per target, so a
// single unreachable host does not abort the rest of the batch. The operation's
// permission is checked by the caller (e.g. the Control Plane) before fan-out;
// Batch itself does not perform authorization.
func (d *Dispatcher) Batch(ctx Context, opName string, targets []TargetHost, input map[string]any) []BatchResult {
	results := make([]BatchResult, 0, len(targets))
	for _, t := range targets {
		// Phase 2.6: enrich each fan-out target with its observed capability
		// snapshot (best-effort SSH probe, cached) so the Resolver consumes the
		// real host capabilities rather than the dominant Linux tool default.
		tctx := EnrichContextForTarget(WithTarget(ctx, t), t)
		res := d.Execute(tctx, opName, input)
		br := BatchResult{Target: t, Success: res.Success, Result: res}
		if res.Error != nil {
			br.Error = res.Error.Error()
		}
		results = append(results, br)
	}
	return results
}
