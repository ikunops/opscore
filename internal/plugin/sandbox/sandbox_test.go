package sandbox

import (
	"errors"
	"testing"
	"time"

	"github.com/YuDong999/opscore/internal/core"
)

// recordingSink captures sandbox decisions for assertions.
type recordingSink struct {
	decisions []Decision
}

func (r *recordingSink) record(d Decision) { r.decisions = append(r.decisions, d) }

type planHandler struct {
	perm  core.Permission
	risk  core.RiskLevel
	steps int
}

func (h planHandler) Plan(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
	steps := make([]core.ExecutionStep, 0, h.steps)
	for i := 0; i < h.steps; i++ {
		steps = append(steps, &core.CommandStep{Name: "s", Executable: "true"})
	}
	return &core.ExecutionPlan{
		OperationName: "op.test",
		Permission:    h.perm,
		Risk:          h.risk,
		Steps:         steps,
	}, nil
}

func baseOp() core.Operation {
	return core.Operation{
		Name:       "op.test",
		Permission: core.Permission{ResourceType: "test", Action: "run"},
		Risk:       core.RiskMedium,
	}
}

func testCtx() core.Context { return core.NewContext().Build() }

func TestWrap_AllowsMatchingPlan(t *testing.T) {
	sink := &recordingSink{}
	env := DefaultEnvelope(baseOp()).WithAudit(sink.record)
	h := Wrap(planHandler{perm: core.Permission{ResourceType: "test", Action: "run"}, risk: core.RiskLow, steps: 1}, env)

	plan, err := h.Plan(testCtx(), nil)
	if err != nil {
		t.Fatalf("unexpected deny: %v", err)
	}
	if plan == nil {
		t.Fatal("expected a plan")
	}
	if len(sink.decisions) == 0 || !sink.decisions[0].Allowed {
		t.Fatalf("expected allow, got %+v", sink.decisions)
	}
}

func TestWrap_DeniesPermissionEscalation(t *testing.T) {
	env := DefaultEnvelope(baseOp())
	h := Wrap(planHandler{perm: core.Permission{ResourceType: "secret", Action: "delete"}, risk: core.RiskLow}, env)

	_, err := h.Plan(testCtx(), nil)
	if err == nil || !contains(err.Error(), "permission escalation") {
		t.Fatalf("expected permission escalation deny, got %v", err)
	}
}

func TestWrap_DeniesRiskEscalation(t *testing.T) {
	env := DefaultEnvelope(baseOp()) // declared RiskMedium
	h := Wrap(planHandler{perm: baseOp().Permission, risk: core.RiskCritical}, env)

	_, err := h.Plan(testCtx(), nil)
	if err == nil || !contains(err.Error(), "risk escalation") {
		t.Fatalf("expected risk escalation deny, got %v", err)
	}
}

func TestWrap_DeniesTooManySteps(t *testing.T) {
	env := NewEnvelope(baseOp(), 2, 0, 0) // MaxSteps = 2
	h := Wrap(planHandler{perm: baseOp().Permission, risk: core.RiskLow, steps: 5}, env)

	_, err := h.Plan(testCtx(), nil)
	if err == nil || !contains(err.Error(), "steps") {
		t.Fatalf("expected step-limit deny, got %v", err)
	}
}

func TestWrap_ExecTimeout(t *testing.T) {
	env := DefaultEnvelope(baseOp())
	env.ExecTimeout = 20 * time.Millisecond
	slow := core.HandlerFunc(func(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
		time.Sleep(200 * time.Millisecond)
		return &core.ExecutionPlan{OperationName: "op.test", Permission: baseOp().Permission, Risk: core.RiskLow}, nil
	})
	h := Wrap(slow, env)

	_, err := h.Plan(testCtx(), nil)
	if err == nil || !contains(err.Error(), "exec timeout") {
		t.Fatalf("expected exec timeout deny, got %v", err)
	}
}

func TestWrap_PropagatesHandlerError(t *testing.T) {
	env := DefaultEnvelope(baseOp())
	boom := core.HandlerFunc(func(ctx core.Context, input map[string]any) (*core.ExecutionPlan, error) {
		return nil, errors.New("kaboom")
	})
	h := Wrap(boom, env)
	if _, err := h.Plan(testCtx(), nil); err == nil || err.Error() != "kaboom" {
		t.Fatalf("expected handler error propagated, got %v", err)
	}
}

func TestWrap_Idempotent(t *testing.T) {
	env := DefaultEnvelope(baseOp())
	h := Wrap(planHandler{perm: baseOp().Permission, risk: core.RiskLow}, env)
	if _, ok := h.(*sandboxHandler); !ok {
		t.Fatal("expected wrapped handler")
	}
	// Wrapping again must NOT double-wrap.
	h2 := Wrap(h, env)
	if _, ok := h2.(*sandboxHandler); !ok {
		t.Fatal("idempotent wrap should return the same sandboxHandler")
	}
	if h != h2 {
		t.Fatal("idempotent wrap should return the identical handler")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
