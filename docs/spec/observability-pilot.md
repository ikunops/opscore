# Observability Pilot — Implementation Spec (Phase 8.1)

> Companion to **ADR-015** (Phase 8.1 Observability Architecture, signed Round 40)
> and **ADR-018** Round 43 decision: implement `internal/observability` as the
> **Pilot Capability** — the first of the four Platform Operations capabilities
> to be coded, chosen because it is the purest Read Model and proves the
> peripheral-package pattern + AST guard before Cluster / Enterprise / Governance.

## Package: `internal/observability`

A peripheral READ MODEL over the frozen systems. It consumes events already
emitted by frozen subsystems and never opens an execution path (ADR-015 MUST-1..5).

| File | Responsibility |
|---|---|
| `model.go` | `Observation` unified read-model record; `Source` / `Kind` enums. Carries **existing IDs only** (TraceID / ExecutionID / RequestID / PluginID / AuditID) — no new identity system. |
| `collector.go` | `Collector`: single ingestion point `record()` + four `Observe*` methods; derives label-keyed counters. |
| `query.go` | Read API: `Query`, `Counter`, `Counters`, `Count`. Read-only, never mutates the store. |
| `sinks.go` | Adapter sinks that wire the collector into **existing** interfaces: `ExecutionBus` (execution.EventBus), `SandboxSink` (sandbox.AuditSink), `SignatureSink` (manifest.AuditSink), `AuditSinkAdapter` (core.AuditSink). |
| `observability_test.go` | **AST guard** (forbids `internal/plugin/runtime` + `internal/plugin/isolation`) + behavior test ingesting all four sources. |

## Event sources consumed (all from frozen systems, ADR-015 MUST-3)

| Frozen event | Adapter | Mapped to |
|---|---|---|
| `execution.ExecutionEvent` (lifecycle) | `ExecutionBus.Publish` | `SourceExecution` trace |
| `sandbox.Decision` (Phase 6.1 envelope) | `SandboxSink` | `SourceSandbox` metric (allow/deny) |
| `manifest.SignatureResult` (Phase 5 trust) | `SignatureSink` | `SourceSignature` metric (verified/unverified) |
| `core.AuditEvent` (compliance) | `AuditSinkAdapter.Emit` | `SourceAudit` correlation |

## Wiring (host side — no frozen-subsystem change required)

```go
col := observability.NewCollector()

// execution lifecycle: subscribe the existing bus
bus := server.NewExecutionEventBus()
bus.SubscribeTo(col)            // hypothetical; or wrap: execution.EventBus = observability.NewExecutionBus(col)

// sandbox envelope (Phase 6.1)
env := env.WithAudit(observability.SandboxSink(col))

// signature verifier (Phase 5)
verifier := manifest.NewSignatureVerifier(policy, observability.SignatureSink(col))

// audit (core)
auditPipeline.AddSink(observability.NewAuditSinkAdapter(col))

// read
metrics := col.Counters()
linked := col.Query(observability.Query{ExecutionID: "exe-1"})
```

Observability adapts; the four frozen subsystems keep their original signatures.

## Freeze compliance

- **MUST-1** Read-only, no execution path — `Collector` holds no execution state, never calls Runtime Core.
- **MUST-2** No Runtime Contract change — zero Contract types added or modified.
- **MUST-3** Data from existing systems only — every field copied from a frozen event; no new execution protocol.
- **MUST-4** Existing IDs only — `TraceID`/`ExecutionID`/`RequestID`/`PluginID`/`AuditID` are all sourced from the frozen events; `ObsID` is an opaque local handle, not an identity system.
- **MUST-5** Composition not intrusion — adapter sinks only; no instrumentation injected into Execution/Manager/Loader/Provider/Manifest/isolation. **AST guard forbids `internal/plugin/runtime` + `internal/plugin/isolation`.**

## Dashboard API (read surface)

`Query` filters by any combination of `TraceID` / `ExecutionID` / `PluginID` /
`Source` / `Kind`. `Counter` / `Counters` expose derived aggregates
(`observations_total`, `verdict_total`, `execution_status_total`). A future
HTTP/Dashboard layer consumes these without ever touching the Runtime.
