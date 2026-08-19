# ADR-003: Lightweight Architecture Principle

**Status**: Accepted
**Date**: 2026-07-21

## Context

The user's original motivation for OpsCore was to reduce burden — "为了减负". They explicitly requested a lightweight system, comparing it to 8-bit NES games that reused tiles and palettes to build rich worlds with minimal resources.

ChatGPT confirmed this is not just a preference — it maps to established engineering principles: Unix philosophy, Kubernetes declarative control plane, SQLite embedded philosophy, Linux init reliability.

## Decision

Adopt five lightweight principles as architectural constraints:

### Principle 1: Single Binary First
Prefer single-process deployment. One binary contains Core + Control Plane + Builtin.
```
scp opscore && ./opscore
```
No microservices unless a measurable bottleneck demands it.

### Principle 2: Progressive Complexity
Complexity grows with need, not in advance. Phase 0 has 6 abstractions in one package, not 9 in nine packages. Split only when the second developer arrives or real complexity emerges.

### Principle 3: Reusable Primitives
Prefer reuse over creation. One Context interface covers CLI/SDK/REST/Plugin. One Handler interface covers Builtin and Plugin. One ExecutionStep interface covers Command and future Docker/HTTP/SSH.

**8-bit analogy**: Like tile reuse in NES games — same primitive, different context.

### Principle 4: Embedded Default
Default to zero external dependencies:
- Phase 0: No DB (AuditSink = LogSink)
- Phase 1: SQLite (modernc.org/sqlite, pure Go, no CGO)
- Production: PostgreSQL (optional, when scale demands it)

Never require an external service to start.

### Principle 5: Capability Driven
Behavior is determined by detected capabilities, not configuration files.
```go
if cap.HasSystemctl {
    plan.AddStep(systemctl.Restart(name))
} else if cap.ServiceManager == "service" {
    plan.AddStep(service.Restart(name))
}
```
No "configure your service manager in config.yaml". The system detects and adapts.

## Consequences

- Binary size target: < 10MB (Phase 0: 3.6MB)
- Memory footprint: runnable on 1C1G
- Deployment: single file, no installer
- Phase 0 external Go dependencies: zero
- Codebase target: < 1500 lines for Core
