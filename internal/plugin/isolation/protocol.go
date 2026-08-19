// Package isolation implements Phase 6.3 Process Isolation: running a
// plugin's Handler.Plan in a HELPER PROCESS reached over a stdio RPC, so that
// a misbehaving plugin can be truly terminated and cannot take the host down.
//
// Why this is possible at all: OpsCore separates PLAN from EXECUTE. A handler
// does not perform work, it returns an ExecutionPlan, which is pure data. That
// makes the process boundary natural — the plan is serializable by
// construction, and the trusted Executor in the host process is what actually
// runs the steps. Isolation therefore fences off *planning logic* (arbitrary
// plugin code) while leaving execution exactly where it already was.
//
// Frozen scope (GPT Round 27 directive) — "Process Isolation", not "Sandbox",
// not "Dynamic Plugin":
//
//	MUST-1  Handler.Plan() keeps its signature; no interface changes.
//	MUST-2  Manager / Registry / Reload / Watcher never learn that a helper
//	        process exists; they only ever see a core.Handler.
//	MUST-3  Timeout is upgraded from "the caller gives up" (Phase 6.1) to
//	        REAL TERMINATION: the helper process is killed.
//	MUST-4  A helper crash must not affect Manager / Dispatcher / Registry.
//	MUST-5  Every failure path is fail-closed (no plan is ever returned).
//
// Explicitly NOT in 6.3: .so / Go plugin, WASM, OCI runtime, containers,
// seccomp, ptrace, cgroup, namespace. Those remain deferred.
//
// Runtime Contract impact: ZERO. This package adds a peripheral decorator and
// a wire format; it changes no Contract type.
//
// Phase 7.1: the wire types and framing now live in the public, core-free
// package ecosystem/sdk (the single source of truth for opscore.isolation/v1).
// This file keeps ONLY the host-side bridge between core.Context /
// core.ExecutionPlan and the SDK's wire types — the part that legitimately
// needs core. The wire bytes are byte-identical to Phase 6.3/6.4.
package isolation

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/YuDong999/opscore/ecosystem/sdk"
	"github.com/YuDong999/opscore/internal/core"
)

// ProjectContext builds the credential-free projection of a core.Context,
// expressed in the SDK's wire types (which carry no core dependency).
func ProjectContext(ctx core.Context) sdk.ContextProjection {
	p := sdk.ContextProjection{}
	if ctx == nil {
		return p
	}
	u := ctx.User()
	p.UserID, p.UserName, p.UserRole = u.ID, u.Name, u.Role
	h := ctx.Host()
	p.Hostname, p.OS, p.Arch = h.Hostname, h.OS, h.Arch
	if t := ctx.Target(); !t.IsZero() {
		// Address / Port / User only — Password, KeyPath and KeyBytes are
		// intentionally NOT projected. See the SDK ContextProjection doc.
		p.TargetAddress, p.TargetPort, p.TargetUser = t.Address, t.Port, t.User
	}
	// Phase 6.4 Execution Projection: copy the host-observed snapshots as
	// opaque JSON (read-only, never re-detected). The SDK carries them as
	// json.RawMessage so it stays free of core's snapshot types.
	if cs := ctx.CapabilitySnapshot(); cs != nil {
		if b, err := json.Marshal(cs); err == nil {
			p.CapabilitySnapshot = b
		}
	}
	if hs := ctx.HostSnapshot(); hs != nil {
		if b, err := json.Marshal(hs); err == nil {
			p.HostSnapshot = b
		}
	}
	p.TraceID = ctx.ExecutionID()
	p.RequestID = ctx.ExecutionID()
	return p
}

// EncodePlan converts an ExecutionPlan into its wire form. It fails if the
// plan holds a step kind with no wire representation (fail-closed).
func EncodePlan(p *core.ExecutionPlan) (*sdk.PlanWire, error) {
	if p == nil {
		return nil, fmt.Errorf("isolation: nil plan")
	}
	w := &sdk.PlanWire{
		OperationName: p.OperationName,
		Risk:          int(p.Risk),
		TimeoutNanos:  int64(p.Timeout),
		Source:        p.Source,
		Origin:        p.Origin,
	}
	w.Permission.ResourceType = p.Permission.ResourceType
	w.Permission.Action = p.Permission.Action

	for i, s := range p.Steps {
		cs, ok := s.(*core.CommandStep)
		if !ok {
			return nil, fmt.Errorf(
				"isolation: step %d of %T is not serializable across the process boundary "+
					"(only %q steps may cross)", i, s, sdk.StepKindCommand)
		}
		w.Steps = append(w.Steps, sdk.StepWire{
			Kind: sdk.StepKindCommand,
			Command: &sdk.CommandWire{
				Name:         cs.Name,
				ID:           cs.ID,
				Index:        cs.Index,
				Executable:   cs.Executable,
				Args:         cs.Args,
				Env:          cs.Env,
				WorkingDir:   cs.WorkingDir,
				TimeoutNanos: int64(cs.Timeout),
			},
		})
	}
	return w, nil
}

// DecodePlan rebuilds an ExecutionPlan from its wire form. An unknown step
// kind is rejected rather than skipped.
func DecodePlan(w *sdk.PlanWire) (*core.ExecutionPlan, error) {
	if w == nil {
		return nil, fmt.Errorf("isolation: nil plan on the wire")
	}
	p := &core.ExecutionPlan{
		OperationName: w.OperationName,
		Permission: core.Permission{
			ResourceType: w.Permission.ResourceType,
			Action:       w.Permission.Action,
		},
		Risk:    core.RiskLevel(w.Risk),
		Timeout: time.Duration(w.TimeoutNanos),
		Source:  w.Source,
		Origin:  w.Origin,
	}
	for i, s := range w.Steps {
		if s.Kind != sdk.StepKindCommand || s.Command == nil {
			return nil, fmt.Errorf("isolation: step %d has unsupported kind %q", i, s.Kind)
		}
		c := s.Command
		p.Steps = append(p.Steps, &core.CommandStep{
			Name:       c.Name,
			ID:         c.ID,
			Index:      c.Index,
			Executable: c.Executable,
			Args:       c.Args,
			Env:        c.Env,
			WorkingDir: c.WorkingDir,
			Timeout:    time.Duration(c.TimeoutNanos),
		})
	}
	return p, nil
}
