# ADR-002: Nine Permanent Abstractions

**Status**: Accepted (V1 Frozen)
**Date**: 2026-07-21

## Context

Through three rounds of ChatGPT architecture review, the abstraction model evolved from 6 core concepts to 9 permanent abstractions. The key insight was separating "what to execute" (static plan) from "how it's going" (runtime state).

## Decision

Freeze 9 permanent abstractions as the V1 architecture:

```
Context          // 运行上下文 (User/Host/Capability/Trace/Logger)
    │
Dispatcher       // 路由 (Operation name → Handler)
    │
Handler          // 业务入口 (returns Plan, not executes)
    │
Planner          // 执行规划 (Phase 0: merged into Handler.Plan())
    │
ExecutionPlan    // 静态执行计划 (Immutable, []ExecutionStep)
    │
PlanRuntime      // 运行时状态 (Mutable, Phase 0: internal to Executor)
    │
Executor         // 执行器 (runs steps sequentially, emits audit)
    │
ExecutionStep    // 原子执行单元 (CommandStep is first impl)
    │
AuditSink        // 事件输出 (Emit interface, LogSink default)
```

**Command is downgraded** to ExecutionStep's first implementation. Future: DockerStep, HTTPStep, SSHStep, K8sStep.

### Phase 0 Merging

Phase 0 does NOT create 9 separate packages. It merges into a flat `core/` package with ~10 files:

| Abstraction | Phase 0 Implementation |
|-------------|----------------------|
| Context | `context.go` — single interface + Builder |
| Dispatcher | `dispatcher.go` — Plan() + Execute() |
| Handler | `handler.go` — interface with Plan() method |
| Planner | Merged into Handler.Plan() — future split preserves API |
| ExecutionPlan | `plan.go` — struct with Steps []ExecutionStep |
| PlanRuntime | Internal to Executor — `executionState` struct |
| Executor | `executor.go` — sequential step runner |
| ExecutionStep | `step.go` — interface + CommandStep impl |
| AuditSink | `audit.go` — Emit interface + LogSink |

### Key Design Rules

1. **Handler.Plan() not Handler.Build()** — future split to Planner.Plan() preserves API
2. **Plan is immutable** — runtime state never pollutes the plan
3. **DryRun = Dispatcher.Plan()** — no separate dry-run engine
4. **Plan is never cached** — depends on Context (OS/version/capability)
5. **Dispatcher only knows Handler interface** — no `if plugin` branch

## Consequences

- Adding a new capability = adding a Handler + Step type
- Platform differences handled by Planner (future), not by Dispatcher
- Pause/Resume/Retry/Rollback all operate on PlanRuntime, not Plan
- Future Step types (Docker/HTTP/SSH) don't change Executor or Dispatcher
