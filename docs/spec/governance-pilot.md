# Governance Pilot — `internal/governance` (Phase 8.4)

Pilot implementation of ADR-018 (Governance / Policy Evaluation) as a peripheral package that
composes the frozen/accepted systems by reference and emits a deterministic `Verdict`. This is the
fourth and final Phase 8 capability; with it, Phase 8 reaches architecture + implementation double
closure across all four layers.

## 1. Package shape

```
internal/governance/
├── model.go          // Verdict (Value Object), VerdictCode, Policy, Rule, RuleKind, State
├── governance.go     // Engine (stateless) + Evaluate(policy, state) → Verdict (pure fn)
├── errors.go         // input-guard errors only (no execution errors)
└── governance_test.go// AST guard + TestNoExecMethod + behavior/determinism/explainability
```

## 2. Contract (ADR-018 MUST-1..5 + SHOULD-1/2)

- **MUST-1 Evaluate only.** `Engine` exposes `Evaluate(policy, state)` and `NewEngine()`. No
  `Execute/Run/Apply/Rollback/Schedule/Emit` (enforced by `TestNoExecMethod`).
- **MUST-2 Verdict only.** Output is a `Verdict` with `Code ∈ {Allow, Deny, RequireApproval,
  MaintenanceBlocked}`. Never an Action/Command.
- **MUST-3 References IDs, owns nothing.** `State` and `Policy` are keyed by existing IDs only
  (`PluginID`, `RuntimeID`, `Group`, `ExecutionID`, `Tenant`). No copy of Runtime/Host/Plugin state.
- **MUST-4 Deterministic.** Rules evaluated in a stable total order (Priority desc, then RuleID asc
  via `sort.SliceStable`). Same `(Policy, State)` ⇒ same `Verdict`, no hidden state / clock /
  side effects (`TestEvaluateDeterministic`).
- **MUST-5 Transport/runtime/plugin agnostic, stdlib only, AST-guarded.** Forbids importing
  `runtime`/`isolation`/`hostregistry`/`cluster`/`observability`/`enterprise` (enforced by
  `TestASTGuardForbiddenImports`).
- **SHOULD-1 Explainable Verdict.** Every non-default verdict carries `Reason`, `MatchedPolicy`,
  `MatchedRule`, `Priority`, `Evidence` (`TestExplainableVerdict`).
- **SHOULD-2 Stable Verdict Contract.** `Verdict` is a frozen Value Object (struct of plain data),
  asserted by `TestVerdictIsFrozenValueObject`. Shape fixed from day one.

## 3. Evaluation model

A `Policy` is an ordered set of `Rule`s. Each `Rule` has a `Kind` (`maintenance-window`,
`change-freeze`, `require-approval`, `tenant-scope`, `group-allow`) and an optional `Param`. The
engine:

1. Sorts rules deterministically.
2. Walks them; the first matching rule emits its `VerdictCode` (with explainability fields).
3. If none match, defaults to `Allow` (never blocks by omission).

This keeps each rule tiny and pure; complexity lives in how policies compose rules, not in each rule.

## 4. Frozen-boundary compliance

- No import of frozen/accepted systems (AST guard).
- No execution path; the verdict is consulted by the Runtime's existing execution interface
  (`Governance → Verdict → Runtime Existing Execution Interface → Execution`).
- No new identity system; reuses existing IDs (ADR-015 MUST-4).

## 5. Enterprise / Governance split (ADR-017 SHOULD-1, ADR-018 MUST-1)

- **Enterprise** (`internal/enterprise`) owns *attachment*: who/what a policy applies to (org scope).
- **Governance** (`internal/governance`) owns *evaluation*: compute the verdict from policy + state.

Governance reads policy metadata (attached by Enterprise) and observed state (assembled from
Observability + Cluster), and emits a verdict. Neither absorbs the other.

## 6. Gate

`gofmt` clean, `go build ./...`, `go vet ./...`, `go test ./...` — all 43 packages green
(including `internal/governance`).
