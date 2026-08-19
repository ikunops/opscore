package core

import "time"

// Permission is a structured permission: ResourceType x Action.
// Never use string parsing — always compare struct fields.
type Permission struct {
	ResourceType string
	Action       string
}

func (p Permission) String() string {
	return p.ResourceType + "." + p.Action
}

// RiskLevel classifies how dangerous an operation is.
type RiskLevel int

const (
	RiskLow RiskLevel = iota
	RiskMedium
	RiskHigh
	RiskCritical
)

func (r RiskLevel) String() string {
	switch r {
	case RiskLow:
		return "low"
	case RiskMedium:
		return "medium"
	case RiskHigh:
		return "high"
	case RiskCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// ExecutionPlan is an immutable description of what should be executed.
// Built by Handler.Plan(), consumed by Executor.
// Never mutated after creation — runtime state lives in Executor internally.
type ExecutionPlan struct {
	OperationName string
	Steps         []ExecutionStep
	Permission    Permission
	Risk          RiskLevel
	Timeout       time.Duration

	// Source is the operation's registration origin ("builtin" |
	// "system" | "plugin:<name>"), copied from the registered Operation
	// by the Dispatcher so the Executor can stamp it onto the record
	// (Phase 3.0 / MUST-1) without a Storage join.
	Source string

	// Origin is WHO triggered this execution ("API" | "CLI" | "BATCH" |
	// "INTERNAL" | "PLUGIN"). Set by the entry point (Runtime.Run => CLI,
	// the Execution API handler => API) so the record/Audit capture it
	// (Phase 3.0 / SHOULD).
	Origin string
}

// ExecutionStep is one atomic unit of work within a plan.
// Implementations: CommandStep (Phase 0), HTTPStep/DockerStep (future).
type ExecutionStep interface {
	Execute(ctx Context) StepResult
	Describe() string
}

// StepResult records the outcome of a single step.
type StepResult struct {
	StepName string
	StepID   string // stable id assigned by the step (or derived from Index)
	Index    int    // position within the plan
	Success  bool
	Output   string
	Error    error
	Duration time.Duration
}

// ExecutionResult is the aggregate result of running an ExecutionPlan.
type ExecutionResult struct {
	Success  bool
	Output   string
	Error    error
	Duration time.Duration
	Steps    []StepResult

	// Cancelled is true when the run was aborted by cancellation (external
	// Cancel(id) or parent-context teardown) rather than by a step failure.
	// It distinguishes a deliberate stop from a FAILED outcome.
	Cancelled bool
}
