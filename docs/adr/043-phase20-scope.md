# ADR-043 — Phase 20: Causal Tracing (Scope)

- **Status**: **ACCEPTED WITH MODIFICATIONS (R90 verdict B)**. Phase 19 = *Evidence Consumers* is
  **CLOSED** (R87-A / R88-A / R89-A, commit `9d1abbb`). R90 authorised **two modifications** that
  must be frozen in ADR-044 (Architecture): **R20-6 rewrite** (explicit trace identity) and
  **R20-9 addition** (no implicit sampling). **No implementation until ADR-044 is signed.**
- **Date**: 2026-08-10
- **Companion to**: ADR-041/042 (Phase 19, CLOSED), ADR-039/040 (Phase 18, CLOSED),
  ADR-022 (Phase 10 Correlation, CLOSED), ADR-015 (Observability Architecture, CLOSED),
  ADR-021 (three-tier discipline, frozen)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 20 — the **causal tracing** layer that makes execution chains
  reconstructable. Phase 19 made evidence *readable at scale*; Phase 20 makes execution
  *understandable as a chain*.

---

## 0. Abstract

Phase 19 is complete. The system can now expose metrics (exact counters), page through audit
history (cursor), project policy activity, and retain scan history — all read-only, all honest.

But the system still cannot answer a fundamental operational question:

> **"A request came in, an execution ran, a plugin was loaded, a sandbox decision was made, an
> audit event was written — how are these causally connected, and what was the duration of each
> step?"**

The existing `Observation` model (ADR-015) carries a `TraceID` field that is **never populated**.
The `KindTrace` constant exists but `ObserveExecution` copies `ev.ID` as both `ExecutionID` and
`RequestID` — there is no actual trace propagation, no span lifecycle, no parent-child
relationship, and no causal ordering.

Phase 20 is therefore proposed as **Causal Tracing**: a minimal, self-contained trace model that
establishes span lifecycle, propagates context via Go's `context.Context`, reuses existing IDs
as span references, and exposes traces as a read-only surface on `:8082`. It adds **zero** new
external dependency, modifies **zero** frozen packages, and stays strictly within the
observation model — it is a *reader* of execution, never a participant.

> Phase 17: the system cannot lie about what it *did*.
> Phase 18: the system cannot lie about what it *sees*.
> Phase 19: the system lets you *read what it sees* — at scale.
> Phase 20: the system lets you *follow what it did* — as a chain.

---

## 1. Positioning — why "causal tracing" and not "OpenTelemetry"

### 1.1 The deferred promise

ADR-015 §1 listed "Traces" as one of four observation signals: "Request / Execution spans (if a
span ID already exists) — Causality across boundaries." The parenthetical "if a span ID already
exists" was a deliberate punt: ADR-015 scoped the *observation model*, not trace propagation.

Phase 19 (ADR-041 §3.4) explicitly scoped tracing OUT:

> "Distributed tracing / OpenTelemetry propagation — new dependency + cross-cutting scope;
> deferred to its own Scope ADR."

GPT's R87 sign-off expanded on why:

> "Tracing is not merely another consumer. It introduces: propagation semantics, context
> ownership, lifecycle rules, sampling policy, external protocol decisions, dependency decisions.
> Those are architecture-level concerns unrelated to read-model consumption. Tracing should enter
> through a separate scope ADR."

Phase 20 is that separate Scope ADR.

### 1.2 What "causal" means

OpsCore is a **single-process** system. There is no distributed RPC mesh. The "causality across
boundaries" that ADR-015 referenced is the boundary between subsystems *within one process*:

```
Request → Execution → Plugin Load → Sandbox Decision → Audit Write
```

Each arrow is a cause→effect relationship within the same process. The trace model must capture
this chain — not a distributed span tree across network hops, but a **causal chain of
subsystem transitions** within OpsCore's own execution path.

### 1.3 Why NOT OpenTelemetry

GPT's R87 enumerated the concerns tracing introduces. Here is how Phase 20 addresses each,
**without** adopting OpenTelemetry:

| Concern (GPT R87) | Phase 20 resolution | OTel alternative (rejected) |
|---|---|---|
| **Propagation semantics** | Go `context.Context` with a `trace.Span` value — the standard Go idiom, zero dependency | OTel `propagation.TraceContext` + W3C headers |
| **Context ownership** | The *caller* owns the context; the trace model is a passenger, never a driver | OTel `TracerProvider` global singleton |
| **Lifecycle rules** | `Span` has `Start()`, `End()`, `Duration()` — three methods, no inheritance complexity | OTel `Span` interface + `SpanKind` + `Status` + `Attributes` |
| **Sampling policy** | Head-based, configurable ratio, bounded ring — consistent with Phase 19's S-4 ring | OTel `Sampler` + `ParentBased` + `TraceIDRatio` |
| **External protocol decisions** | Read-only `GET /management/v1/traces` returning JSON — no OTLP, no Jaeger, no Zipkin | OTLP exporter + collector pipeline |
| **Dependency decisions** | **Zero new dependencies.** `context`, `sync`, `time` — all stdlib | `go.opentelemetry.io/otel` + 6+ transitive modules |

The rejection of OpenTelemetry is not dogma — it is **scope discipline**. OTel is designed for
distributed systems with network boundaries, cross-service propagation, and vendor-specific
export pipelines. OpsCore has none of these. Adopting OTel would import a distributed-tracing
framework to solve a single-process causal-chain problem, exactly the kind of scope inflation
ADR-039 and ADR-041 were created to prevent.

If OpsCore ever becomes distributed (cluster execution across nodes), a future Scope ADR can
revisit OTel with a concrete propagation requirement. Today there is no such requirement.

---

## 2. Evidence for the gap (code as of `9d1abbb`)

### 2.1 T-1 — TraceID is vestigial

`Observation.TraceID` (model.go:89) is declared but **never set** by any `Observe*` method:

| Method | Sets TraceID? | Evidence |
|---|---|---|
| `ObserveExecution` | ❌ | Copies `ev.ID` as `ExecutionID` and `RequestID`; TraceID = "" |
| `ObserveSandbox` | ❌ | No TraceID field on `sandbox.Decision` |
| `ObserveSignature` | ❌ | No TraceID field on `manifest.SignatureResult` |
| `ObserveAudit` | ❌ | Copies `a.TraceID` — but `core.AuditEvent.TraceID` is itself never populated |

The field exists as a type-system promise that was never fulfilled. Phase 20 fulfills it.

### 2.2 T-2 — No span lifecycle

There is no concept of a "span" — a unit of work with a start time, end time, duration, and
parent reference. The `Observation` model captures *point-in-time events* (one event = one
observation), not *intervals* (start → end). An operator cannot ask "how long did the plugin
load phase take within this execution?" because there is no span boundary.

### 2.3 T-3 — No causal chain

The correlation package (ADR-022, Phase 10) joins events by **shared ID** — it finds all
observations with the same `ExecutionID`. But correlation is not causation:

- Correlation: "these events share an ExecutionID"
- Causation: "the sandbox decision was *caused by* the plugin load, which was *caused by* the
  execution request"

There is no `ParentSpanID` or causal ordering. An operator reconstructing the chain must infer
causality from timestamps — fragile, non-deterministic, and impossible under concurrent
executions.

### 2.4 T-4 — No trace export surface

Phase 19 added `GET /management/v1/metrics` and `GET /management/v1/projections/policy-activity`.
There is no `GET /management/v1/traces` — no way to retrieve a trace tree for a given
`ExecutionID` or `TraceID`. The trace data (if it existed) would be invisible.

---

## 3. Phase 20 scope — Causal Tracing

### 3.1 Candidate scope items (proposed)

| # | Item | Fixes | Surface |
|---|---|---|---|
| S-1 | **Span model & lifecycle.** A `Span` type with `TraceID`, `SpanID`, `ParentSpanID`, `Start`, `End`, `Duration`, `Operation`, and a `Refs` map linking to existing IDs (`ExecutionID`, `RequestID`, `PluginID`, `AuditID`). Spans are created via `StartSpan(ctx, operation) (Span, ctx)` and closed via `span.End()`. Parent-child is established by propagating the span through `context.Context`. | T-2, T-3 | `internal/observability` (new `trace.go`) or `internal/tracing` (new package) |
| S-2 | **Context propagation.** A `trace.Context` helper that stores and retrieves the current `Span` from `context.Context` using a private context key. The execution engine's existing `context.Context` parameter is the propagation vehicle — **no new parameter, no new function signature on frozen types**. The trace model is a passenger on the existing context, never a driver. | T-1, T-3 | `internal/observability` or `internal/tracing` |
| S-3 | **Trace ingestion & ring.** When a span ends, it is ingested into a bounded in-memory ring (consistent with Phase 19's S-4 scan-history ring pattern). The ring stores recent complete traces; eviction is counted and surfaced (never silent — the Phase 18 false-clean lesson). Each span carries the existing IDs it observed, so a trace tree can be reconstructed by `TraceID`. | T-2, T-4 | `internal/observability` (collector integration) |
| S-4 | **Trace read surface.** Read-only `GET /management/v1/traces?execution_id=X` (or `?trace_id=X`) returning a JSON trace tree: spans ordered by start time, parent-child relationships explicit, durations included. Bounded result set with `Truncated` flag (consistent with Phase 18/19 vocabulary). Token-gated, `:8082`-only. | T-4 | `internal/management` (`:8082`) |

### 3.2 MUST (Phase 20 iron laws)

- **R20-0** `external/v1` Public Contract **UNCHANGED** (inherit R19-0). No new external route,
  no shape change.
- **R20-1** New surface (`/traces`) lives on `:8082` only, token-gated, behind the existing
  management AuthN/AuthZ. Never `external/v1`.
- **R20-2** **No new dependency.** Trace model is hand-rolled using `context`, `sync`, `time`,
  `crypto/rand` — all stdlib. No OpenTelemetry SDK, no OTLP exporter, no Jaeger client. New deps
  require their own Scope ADR. This directly inherits R19-2's precedent.
- **R20-3** **Frozen packages unmodified** (`platform`, `governance`, `plugin/{runtime,isolation}`,
  `controlplane/hostregistry`, `external`, `governancepolicy`, `platformview`) — `git diff` empty.
  The trace model attaches to execution via the **existing** `context.Context` parameter and the
  **existing** event bus. No frozen type gets a new field, method, or import.
- **R20-4** **No new mutation.** Phase 20 adds no write path, no verb, no state transition.
  Spans are *observations* of work that already happened; they are not a control signal. The
  trace model never calls back into execution, never triggers a plugin load, never issues a
  sandbox decision.
- **R20-5** **Context propagation only — no global state.** The current span is stored in and
  retrieved from `context.Context`. There is no global `CurrentSpan()` function, no goroutine-local
  storage, no `sync.Once` singleton. A span exists only where its context flows. This prevents
  the "which span am I in?" ambiguity that plagues global tracer patterns.
- **R20-6** **TraceID is a trace identity, NOT an `ExecutionID` alias.** `SpanID` is independently
  unique within the trace. Existing IDs (`ExecutionID`, `RequestID`, `CorrelationID`,
  `CapabilityID`, `PluginID`, `AuditID`) are attached as **Refs**, never as identity substitutes.
  A trace may *reference* an execution, but an execution does not *define* the trace identity.
  The trace tree spans Request → execution → Plugin load → Sandbox decision → Operation → audit
  — these spans have different lifetimes and relationships while sharing one trace. **ID
  generation must not introduce another externally visible identity system** (R20-6 rewrite,
  frozen at R90).
- **R20-7** **Span lifecycle is non-blocking.** `StartSpan` and `End()` never block the execution
  path. If the ring is full, the span is counted and dropped (not blocked). The trace model is a
  passenger, never a bottleneck. This is the tracing equivalent of ADR-015 SHOULD-2 (collector
  backpressure boundary).
- **R20-8** **Trace ring is bounded & honest.** The trace ring has explicit capacity, eviction
  semantics, and a `Truncated` flag — consistent with Phase 18's `Complete()` and Phase 19's
  scan-history ring. Absence from the ring ≠ absence from history (the audit store remains the
  durable evidence ledger; the trace ring is an operational convenience).
- **R20-9** **No implicit sampling.** For Phase 20: *eligible span = captured*, until bounded
  storage eviction occurs. Sampling (head-based, tail-based, ratio-based, etc.) is **explicitly
  OUT of scope** for this phase. If sampling is ever introduced, it requires its own Scope +
  Architecture ADR and must define how sampled-out spans are distinguished from nonexistent
  spans — otherwise the Phase 18 false-clean class is recreated (trace absent could mean
  "never happened" or "was sampled out"). (R20-9 addition, frozen at R90.)

### 3.3 SHOULD (Phase 20)

- New management route bumps `RoutePatterns()`; `TestRoutePatternsAreManagementScoped` is updated
  to the new count (currently 10 → 11 for `/traces`).
- Every new law gets a mutation test proven to fail before the fix lands (standing discipline).
- `TraceID` is populated on `Observation` when a span context is active — this fulfills the
  vestigial field (T-1) without changing the `Observation` struct (the field already exists).
- Span duration is measured with `time.Since(start)` — monotonic clock, no wall-clock drift.
- Trace JSON response shape is versioned `trace/view/v1` (consistent with `correlation/view/v1`
  and `observability/v1`).
- Sampling, if implemented in this phase, is head-based and configurable via management config
  (not env var, not flag — consistent with Phase 17.2's token-from-config-only principle).

### 3.4 Out of scope (Phase 20)

- **OpenTelemetry SDK / OTLP exporter** — rejected (§1.3). If distributed execution is ever
  needed, a future Scope ADR can revisit with a concrete propagation requirement.
- **Distributed tracing** — OpsCore is single-process; there are no network boundaries to trace
  across. If cluster execution (ADR-016) ever becomes distributed, tracing scope expands.
- **Dashboards / UI / Grafana** — presentation concern (inherit ADR-015 SHOULD-2, ADR-041 §3.4).
- **Audit retention / archival** — R20-8: trace ring is in-memory, bounded, never persisted.
- **`external/v1` changes** — R20-0.
- **Modifying frozen packages** to add trace instrumentation — R20-3. The trace model observes
  via the existing event bus and context; it does not inject `StartSpan` calls into
  `Execution` / `Manager` / `isolation`. If a frozen system needs to emit span boundaries, that
  is a Contract change requiring its own ADR (ADR-012 freeze boundary).
- **Background trace exporters** — no goroutine that drains traces to an external sink. The
  trace ring is read on-demand via `GET /traces`.

---

## 4. Decision requested (Round 90)

**(A) Accept** Phase 20 = *Causal Tracing* with scope items S-1…S-4 and laws R20-0…R20-8 —
authorising a Phase 20 Architecture ADR (ADR-044), no code before it is signed.

**(B) Accept with modification** — e.g. drop S-4 (trace read surface) as premature, or merge
S-3 (ring) into the existing collector, or insist on a sampling scope item.

**(C) Reject** — e.g. insist OpenTelemetry belongs here (would require R20-2 to fall, need a
dependency justification), or restart as a defect round, or redirect to a different Major
(Control Plane, protection-intention).

**(D) Other.**

Note the OTel rejection is deliberate and put forward for adjudication: GPT's R87 flagged
"dependency decisions" as a concern tracing introduces. This ADR resolves that concern by
rejecting the dependency, exactly as ADR-041 resolved the "tracing trap" by scoping it out.
If GPT adjudicates that OTel is necessary, R20-2 falls and a dependency justification section
is required in ADR-044.

---

## 5. Phase 20 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 20-scope | ADR-043 Phase 20 Scope (this ADR) | **DRAFT — R90 sign-off pending** |
| 20-arch | ADR-044 Phase 20 Architecture | authorized after R90 — blocked |
| 20-impl | Implementation + mutation tests | blocked — after 20-arch sign-off |

Each step authorized only after the previous is signed (ADR-021).

Global ordering (frozen at R84, reaffirmed R89):

```
Phase 17    write correctness / mutation audit integrity      CLOSED
Phase 17.3  reconciliation visibility                         CLOSED
Phase 18    evidence integrity → trustworthy observations     CLOSED
Phase 19    evidence consumers → readable at scale            CLOSED
Phase 20    causal tracing → execution as a chain             ← here
```

---

## 6. Sign-off record

### Round 90 (Scope sign-off)

**Verdict: B — ACCEPT WITH MODIFICATIONS.**

| Item | Verdict | Round |
|---|---|---|
| Phase 20 Scope framing (Causal Tracing after Consumers) | ✅ ACCEPT | 90 |
| S-1 Span model & lifecycle (hand-rolled, no OTel) | ✅ ACCEPT | 90 |
| S-2 Context propagation (context.Context, no global state) | ✅ ACCEPT | 90 |
| S-3 Trace ingestion & ring (bounded, honest eviction) | ✅ ACCEPT | 90 |
| S-4 Trace read surface (GET /management/v1/traces) | ✅ ACCEPT | 90 |
| OpenTelemetry rejection | ✅ ACCEPT | 90 |
| R20-0…R20-5, R20-7, R20-8 iron laws | ✅ ACCEPT | 90 |
| **R20-6 original wording** ("reuses existing ID space") | ⚠️ REWRITE | 90 |
| **R20-9 — no implicit sampling** | ⚠️ ADD | 90 |

**Modifications frozen at R90 (must propagate into ADR-044):**

1. **R20-6 rewrite:** `TraceID` is a **trace identity**, not an `ExecutionID` alias. `SpanID` is
   unique within the trace. Existing IDs (`ExecutionID`, `RequestID`, `CorrelationID`,
   `CapabilityID`, `PluginID`, `AuditID`) are **Refs**, never identity substitutes. A trace may
   reference an execution, but an execution does not define the trace identity. ID generation
   must not introduce another externally visible identity system.
2. **R20-9 addition:** No implicit sampling. *Eligible span = captured*, until bounded storage
   eviction occurs. Sampling requires its own Scope + Architecture ADR and must define how
   sampled-out spans are distinguished from nonexistent spans.

**Critical guard from R90:** "**tracing must never alter execution behavior.** A missing trace
context must not cause execution failure." (Applied to S-2 in §3.1 and required in ADR-044 §3.)

**OpenTelemetry boundary from R90:** "Do not implement a pirate OTel clone." The hand-rolled
model must remain deliberately small (TraceID / SpanID / ParentSpanID / Start / End / Duration /
Refs). No `SpanKind`, exporters, OTLP baggage, vendor propagation, collector protocols, etc.

*Scope signed with modifications. ADR-044 Architecture authorized only after the two
modifications are frozen in its body.*

---

## 7. Carry-forward into ADR-044 (to be filled at R91)

The Round 90 sign-off may add binding clarifications and required answers. Anticipated questions
for ADR-044 to answer:

- **Span struct** — exact field set; whether `Refs` is `map[string]string` or a typed struct;
  whether `SpanID` is 8-byte hex or 16-byte UUID; naming stability.
- **Context key** — private type vs. string key; whether `trace.FromContext` returns
  `(Span, bool)` or `Span` (zero-value = no active span).
- **Ring integration** — whether the trace ring is a separate data structure or a view over the
  existing `Collector` ring; whether `Observation.Kind = KindTrace` spans coexist with point
  observations or are a separate stream.
- **TraceID population** — how `Observation.TraceID` gets populated when a span context is
  active during `Observe*` calls; whether this requires a collector signature change (it must
  not — R20-3 frozen).
- **Trace tree reconstruction** — JSON shape for `GET /traces`; whether it returns a flat list
  (client reconstructs tree from `ParentSpanID`) or a nested tree (server builds it).
- **Sampling** — whether sampling is in scope for Phase 20 or deferred; if in scope, the sampling
  ratio config surface and the head-based decision point.
