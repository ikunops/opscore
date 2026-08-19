# Phase 8.3 Enterprise Operations — Pilot Implementation

> Spec for `internal/enterprise`. Companion to `docs/adr/017-enterprise-operations-architecture.md`.
> This is a **Policy Layer**, not an **Enterprise Runtime** (ADR-017 headline invariant).

## 1. Package shape

```
internal/enterprise/
├── model.go           # TargetKind / PolicyKind / PolicyAttachment (only policy metadata + refs)
├── enterprise.go       # Service: Attach / Detach / AttachmentsFor / All (no exec, no verdict)
├── errors.go           # metadata-layer errors only (never execution failures)
└── enterprise_test.go  # AST guard + behavior (attach/query/detach/validation/no-exec)
```

Pure Go standard library. Imports **no** frozen subsystem.

## 2. Frozen-system references (not ownership)

| Enterprise holds | Owned by |
|---|---|
| `TargetRef` (string) for `host-a` | `controlplane/hostregistry` |
| `TargetRef` for a `plugin-x` | Plugin Ecosystem |
| `TargetRef` for a runtime / execution id | Runtime Core |
| `TargetRef` for a cluster id | `internal/cluster` (ADR-016) |

Enterprise binds `PolicyAttachment{ TargetKind, TargetRef, Kind, Metadata }` to these
**existing IDs** — it never re-records host hardware, membership, plugin binaries, or
runtime state. No new identity system (ADR-015 MUST-4, ADR-017 §3).

## 3. Policy kinds Enterprise owns (ADR-017 §1)

- `PolicyApproval` — weighed action requires explicit approval
- `PolicyMaintenanceWindow` — time window gating actions
- `PolicyTenantScope` — org/tenant/business-unit scope
- `PolicyRBAC` — RBAC extension rule
- `PolicyChangeFreeze` — change-process freeze

None is an execution protocol.

## 4. Boundary enforcement (ADR-017 MUST-1..5)

- **MUST-1 NO EXECUTION**: `Service` has no `Run/Exec/Invoke/Execute/Command` method
  (`TestNoExecMethod`). Enterprise only attaches policy.
- **MUST-2 DOES NOT OWN HOST/CLUSTER/PLUGIN/RUNTIME**: holds opaque `TargetRef` strings only.
- **MUST-3 POLICY METADATA ONLY**: `PolicyAttachment.Metadata` is free-form policy detail,
  never an execution plan.
- **MUST-4 CONSTRAINS, NOT IMPLEMENTS**: `Attach` emits state, not action; the verdict that
  may result is produced by Governance (ADR-018), not here.
- **MUST-5 COMPOSES, NEVER REPLICATES**: AST guard (`enterprise_test.go`) forbids importing
  `internal/plugin/runtime`, `internal/plugin/isolation`, `internal/controlplane/hostregistry`,
  `internal/cluster`, `internal/observability`, `internal/governance`.

## 5. Enterprise / Governance split (ADR-017 SHOULD-1)

`Attach` records *who/what a policy applies to* (org scope). There is **no evaluate method**
in this package — verdict production belongs to `internal/governance` (ADR-018). The split is
enforced by shape, not convention.

## 6. Host wiring example

```go
svc := enterprise.NewService()

// ops attaches a maintenance window to an existing host ref (owns policy, not host)
att, _ := svc.Attach(enterprise.TargetHost, "host-a",
    enterprise.PolicyMaintenanceWindow, map[string]string{"window": "22:00-23:00"})

// later, a caller consults attachments + hands them to Governance for a verdict
for _, a := range svc.AttachmentsFor(enterprise.TargetHost, "host-a") {
    _ = a // → governance.Evaluate(policy, state) → Verdict
}
// Enterprise never executes; the Runtime runs only after a Verdict.
```

## 7. Freeze-compliance checklist

- [x] No exec path; no command method
- [x] No host/cluster/runtime ownership — refs only
- [x] No second inventory, no second runtime
- [x] No evaluation — verdict stays in Governance
- [x] AST guard forbids frozen-system imports
- [x] References only existing IDs (no new identity system)
