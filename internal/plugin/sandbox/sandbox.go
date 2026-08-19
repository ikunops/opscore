// Package sandbox implements the Phase 6.1 peripheral isolation envelope for
// plugin operations. It wraps a plugin's core.Handler so that, BEFORE the
// Executor ever runs a step, the returned plan is checked against the
// plugin's declared permission/risk envelope and resource boundaries, and
// the Plan call itself is bounded by an execution timeout.
//
// Design invariants (GPT Round 25 directive for Phase 6.1):
//   - ZERO Runtime Contract change. It only decorates core.Handler, which is
//     an Operation-as-Code detail, not part of the frozen Contract (Manifest
//     schema, Provider interface, Loader, Descriptor, Module, Manager
//     lifecycle, Compatibility Gate, Capability Negotiation, Reload/Watcher
//     are all untouched).
//   - syscall/process isolation is OUT OF SCOPE for 6.1: in-process Go cannot
//     isolate syscalls from the kernel; real isolation requires .so/container
//     boundaries (Phase 6.3+). It is documented as deferred, with this
//     envelope left as the enforceable in-process layer that any future real
//     sandbox can sit behind.
//   - fail-closed: any envelope violation denies the plan.
//
// Where it is applied (without changing any interface):
//   - runtime.Module.Operations()  — the Manager's registration path
//   - plugin.Module.Register       — the builtin.Module path
//
// Wrap is idempotent so the two paths can never double-wrap a handler.
package sandbox

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/YuDong999/opscore/internal/core"
)

// DecisionCode is a stable, machine-readable classification of a sandbox
// verdict (GPT Round 26 SHOULD). It mirrors the pattern already used by
// CompatibilityResult and manifest.SignatureResult so that UI, Audit and
// Metrics consumers never have to parse the human-readable Reason string.
type DecisionCode string

const (
	CodeAllowed              DecisionCode = "allowed"
	CodeTimeout              DecisionCode = "timeout"
	CodePermissionEscalation DecisionCode = "permission-escalation"
	CodeRiskEscalation       DecisionCode = "risk-escalation"
	CodeStepLimit            DecisionCode = "step-limit"
	CodeInputTooLarge        DecisionCode = "input-too-large"
	CodePlanTooLarge         DecisionCode = "plan-too-large"
	CodePlanError            DecisionCode = "plan-error"
	CodeNilPlan              DecisionCode = "nil-plan"
)

// Decision is a peripheral audit record of one sandbox allow/deny verdict.
// It is deliberately decoupled from the Runtime Audit Contract (mirrors the
// manifest.SignatureVerifier AuditSink pattern): the sandbox OBSERVES, it
// does not write to the Runtime Audit Store directly.
type Decision struct {
	Operation string
	Source    string
	Allowed   bool
	// Code is the stable classification; prefer it over Reason for any
	// programmatic branching, metrics label or audit field.
	Code   DecisionCode
	Reason string
}

// AuditSink receives sandbox decisions. Optional; nil = no audit.
type AuditSink func(Decision)

// Envelope is the isolation boundary for ONE plugin operation. It is derived
// from the operation's manifest declaration and enforced at plan time.
type Envelope struct {
	// OpPermission is the declared (ResourceType, Action) of the operation.
	// A handler that returns a NON-EMPTY plan.Permission differing from this
	// is an escalation attempt -> deny (fail-closed).
	OpPermission core.Permission
	// MaxRisk is the declared risk ceiling. A plan.Risk above this -> deny.
	MaxRisk core.RiskLevel
	// ExecTimeout bounds the Handler.Plan call itself. 0 = no bound.
	ExecTimeout time.Duration

	// --- Resource boundary (SHOULD, opt-in). 0 = unlimited. ---
	// MaxSteps caps the number of steps a plugin may plan.
	MaxSteps int
	// MaxInputBytes caps the serialized input map size.
	MaxInputBytes int
	// MaxPlanBytes caps the serialized plan size.
	MaxPlanBytes int

	// Audit is an optional peripheral observer for allow/deny decisions.
	Audit AuditSink
}

// DefaultEnvelope builds a fail-closed-but-safe envelope for an operation:
// permission + risk escalation checks ON, a 30s ExecTimeout, and resource
// limits OFF (0) so existing behaviour is preserved until a deployment opts
// in via NewEnvelope.
func DefaultEnvelope(op core.Operation) Envelope {
	return Envelope{
		OpPermission: op.Permission,
		MaxRisk:      op.Risk,
		ExecTimeout:  30 * time.Second,
	}
}

// NewEnvelope builds an envelope with explicit resource limits.
func NewEnvelope(op core.Operation, maxSteps, maxInputBytes, maxPlanBytes int) Envelope {
	e := DefaultEnvelope(op)
	e.MaxSteps = maxSteps
	e.MaxInputBytes = maxInputBytes
	e.MaxPlanBytes = maxPlanBytes
	return e
}

// WithAudit attaches a peripheral audit sink.
func (e Envelope) WithAudit(sink AuditSink) Envelope {
	e.Audit = sink
	return e
}

// Wrap decorates a core.Handler with the isolation envelope. Idempotent: if h
// is already a sandbox-wrapped handler, it is returned unchanged.
func Wrap(h core.Handler, env Envelope) core.Handler {
	if _, ok := h.(*sandboxHandler); ok {
		return h
	}
	return &sandboxHandler{inner: h, env: env}
}

type sandboxHandler struct {
	inner core.Handler
	env   Envelope
}

type planResult struct {
	plan *core.ExecutionPlan
	err  error
}

func (s *sandboxHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	// --- Execution timeout (MUST) ---
	// core.Context embeds context.Context but adds methods, so we cannot pass a
	// context.WithTimeout result (which is only a context.Context) into the
	// handler. Instead we race the Plan call against a timer.
	if s.env.ExecTimeout > 0 {
		ch := make(chan planResult, 1)
		go func() {
			p, e := s.inner.Plan(ctx, input)
			ch <- planResult{p, e}
		}()
		timer := time.NewTimer(s.env.ExecTimeout)
		defer timer.Stop()
		select {
		case res := <-ch:
			if res.err != nil {
				s.audit(nil, false, CodePlanError, "plan error: "+res.err.Error())
				return nil, res.err
			}
			return s.enforce(res.plan, input)
		case <-timer.C:
			// NOTE (GPT Round 26 SHOULD): this timeout is fail-closed from the
			// CALLER's perspective only. The goroutine running inner.Plan is
			// NOT killed — Go cannot forcibly cancel a goroutine. It may keep
			// running until it returns on its own. Truly stopping runaway
			// plugin code requires cooperative cancellation or process
			// isolation, which is deferred to Phase 6.3.
			s.audit(nil, false, CodeTimeout, fmt.Sprintf("exec timeout exceeded (%s)", s.env.ExecTimeout))
			return nil, fmt.Errorf("sandbox: handler Plan exceeded exec timeout %s", s.env.ExecTimeout)
		}
	}

	plan, err := s.inner.Plan(ctx, input)
	if err != nil {
		s.audit(nil, false, CodePlanError, "plan error: "+err.Error())
		return nil, err
	}
	return s.enforce(plan, input)
}

func (s *sandboxHandler) enforce(plan *core.ExecutionPlan, input map[string]any) (*core.ExecutionPlan, error) {
	if plan == nil {
		s.audit(nil, false, CodeNilPlan, "nil plan")
		return nil, fmt.Errorf("sandbox: handler returned nil plan")
	}

	// --- Permission envelope (MUST, fail-closed) ---
	// The Dispatcher stamps plan.Permission from the registered op when empty,
	// so a handler returning an EMPTY permission is normal and allowed. Only a
	// handler that returns a NON-EMPTY, MISMATCHED permission is an escalation.
	if plan.Permission.ResourceType != "" || plan.Permission.Action != "" {
		if plan.Permission != s.env.OpPermission {
			s.audit(plan, false, CodePermissionEscalation, fmt.Sprintf("permission escalation: plan %s != declared %s", plan.Permission, s.env.OpPermission))
			return nil, fmt.Errorf("sandbox: permission escalation: plan %s != declared %s", plan.Permission, s.env.OpPermission)
		}
	}

	// --- Risk envelope (MUST, fail-closed) ---
	// Risk is NOT stamped by the Dispatcher, so any value the handler sets is
	// authoritative; refusing to exceed the declared ceiling prevents a plugin
	// from quietly upgrading a "low" op into a "critical" one.
	if plan.Risk > s.env.MaxRisk {
		s.audit(plan, false, CodeRiskEscalation, fmt.Sprintf("risk escalation: plan %s > declared %s", plan.Risk, s.env.MaxRisk))
		return nil, fmt.Errorf("sandbox: risk escalation: plan risk %s > declared %s", plan.Risk, s.env.MaxRisk)
	}

	// --- Resource boundary (SHOULD, opt-in) ---
	if s.env.MaxSteps > 0 && len(plan.Steps) > s.env.MaxSteps {
		s.audit(plan, false, CodeStepLimit, fmt.Sprintf("too many steps: %d > %d", len(plan.Steps), s.env.MaxSteps))
		return nil, fmt.Errorf("sandbox: plan has %d steps > limit %d", len(plan.Steps), s.env.MaxSteps)
	}
	if s.env.MaxInputBytes > 0 {
		if n := approxBytes(input); n > s.env.MaxInputBytes {
			s.audit(plan, false, CodeInputTooLarge, fmt.Sprintf("input too large: %d > %d", n, s.env.MaxInputBytes))
			return nil, fmt.Errorf("sandbox: input %d bytes > limit %d", n, s.env.MaxInputBytes)
		}
	}
	if s.env.MaxPlanBytes > 0 {
		if n := approxPlanBytes(plan); n > s.env.MaxPlanBytes {
			s.audit(plan, false, CodePlanTooLarge, fmt.Sprintf("plan too large: %d > %d", n, s.env.MaxPlanBytes))
			return nil, fmt.Errorf("sandbox: plan %d bytes > limit %d", n, s.env.MaxPlanBytes)
		}
	}

	// Stamp plan.Timeout as a hint for downstream per-step enforcement
	// (CommandStep already honours its own Timeout; this is defence-in-depth
	// for plans that omit it).
	if plan.Timeout == 0 && s.env.ExecTimeout > 0 {
		plan.Timeout = s.env.ExecTimeout
	}

	s.audit(plan, true, CodeAllowed, "allowed")
	return plan, nil
}

func (s *sandboxHandler) audit(plan *core.ExecutionPlan, allowed bool, code DecisionCode, reason string) {
	if s.env.Audit == nil {
		return
	}
	name, src := "", ""
	if plan != nil {
		name = plan.OperationName
		src = plan.Source
	}
	s.env.Audit(Decision{Operation: name, Source: src, Allowed: allowed, Code: code, Reason: reason})
}

// approxBytes estimates the serialized size of an arbitrary input map. On
// marshal failure it returns 0 (callers treat 0 as "could not measure" and
// must NOT deny on a measurement miss).
func approxBytes(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

func approxPlanBytes(plan *core.ExecutionPlan) int {
	b, err := json.Marshal(plan)
	if err != nil {
		return 0
	}
	return len(b)
}
