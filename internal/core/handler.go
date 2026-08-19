package core

// Handler is the business entry point for an operation.
// It does NOT execute anything — it builds an ExecutionPlan.
//
// Phase 0: Handler.Plan() builds the plan directly.
// Future: Handler returns a Planner, Planner builds the plan.
// The method name "Plan" is chosen so this future split changes nothing.
type Handler interface {
	Plan(ctx Context, input map[string]any) (*ExecutionPlan, error)
}

// HandlerFunc is a function adapter for Handler (like http.HandlerFunc).
type HandlerFunc func(ctx Context, input map[string]any) (*ExecutionPlan, error)

func (f HandlerFunc) Plan(ctx Context, input map[string]any) (*ExecutionPlan, error) {
	return f(ctx, input)
}
