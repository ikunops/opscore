# ADR-045 — Phase 20: Causal Tracing (Implementation)

- **Status**: **ACCEPTED at Round 92 (verdict A)** — Implementation authorized. ADR-043 (Scope)
  ACCEPTED WITH MODIFICATIONS at R90 (verdict B). ADR-044 (Architecture) ACCEPTED at R91
  (verdict A) with one implementation constraint, frozen here as R20-10. R92 accepted ADR-045
  and carried one additional invariant ("a trace must never be considered complete merely
  because the queried trace has not been found") which is already consistent with P-12/R20-10,
  so no ADR modification was required. **Implementation commits authorized under the §2 file
  inventory and §6 test plan.** No code was committed before R92 sign-off.
- **Date**: 2026-08-10
- **Companion to**: ADR-043 (Phase 20 Scope, ACCEPTED WITH MODIFICATIONS at R90),
  ADR-044 (Phase 20 Architecture, ACCEPTED at R91), ADR-041/042 (Phase 19, CLOSED),
  ADR-039/040 (Phase 18, CLOSED), ADR-022 (Phase 10 Correlation, CLOSED),
  ADR-015 (Observability Architecture, CLOSED), ADR-021 (three-tier discipline, frozen)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme**: The concrete file-by-file, line-by-line implementation of Phase 20 — everything
  that goes into the repo at the end of this round. Frozen iron laws R20-0..R20-9 must be
  verifiable by `git diff` and by mutation tests.

---

## 0. Abstract

ADR-043 scoped Phase 20 = *Causal Tracing* with nine iron laws (R20-0..R20-9 after R90).
ADR-044 specified the architecture: the `Span` struct, the `internal/tracing` package surface,
the `TraceRing` discipline, the `Observation.TraceID` population path at the adapter sink layer,
and the read surface contract. This ADR-045 specifies the implementation — exact files, exact
line counts, exact test names, exact mutation tests, and the wiring order that keeps the frozen
package set at zero diff.

R91 added one implementation constraint that must be visible in this ADR:

> **Treat the `execution_id → trace_id` lookup as advisory resolution, not implicit identity.**
> If the implementation cannot establish that mapping from captured `Refs`, return 404 — never
> manufacture a trace ID from the execution ID.
> **`truncated=true` must remain attached to a successful trace response whenever the bounded
> ring cannot establish completeness.** Never turn ring eviction into a clean absence.

This ADR freezes that constraint as **R20-10** and adds the corresponding enforcement tests.

---

## 1. Frozen iron laws (binding)

From ADR-043 §3 + R90 modifications + R91 constraint. Any code committed for Phase 20 must
satisfy all eleven:

- **R20-0** `external/v1` surface unchanged — read surface only.
- **R20-1** New routes `:8082`-only + token-gated (`OPSCORE_MANAGEMENT_TOKEN`).
- **R20-2** **No new dependency** — hand-rolled with stdlib only; OTel SDK rejected.
- **R20-3** Frozen package set (`internal/platform`, `internal/governance`,
  `internal/plugin/runtime`, `internal/plugin/isolation`, `internal/controlplane/hostregistry`)
  remains at zero diff.
- **R20-4** No mutation — tracing is observation, not execution.
- **R20-5** Context propagation only — no global state, no goroutine-local.
- **R20-6** `TraceID` is a trace identity, not an `ExecutionID` alias. `SpanID` independently
  unique within trace. Existing IDs are `Refs`, never identity substitutes.
- **R20-7** Span lifecycle non-blocking — `End()` never blocks on I/O.
- **R20-8** Trace ring bounded & honest — `Truncated` flag surfaced; `DroppedCount()` incremented.
- **R20-9** No implicit sampling — *eligible span = captured* until bounded eviction.
- **R20-10** *(new, from R91)* **Advisory resolution.** `execution_id → trace_id` is a
  `Refs`-based lookup, not an identity derivation. Missing `Refs` ⇒ 404, not manufactured
  `TraceID`. **Truncated honesty.** A successful trace response that experienced ring eviction
  MUST carry `truncated: true`. Eviction is never silently dropped from the response.

---

## 2. Implementation map (file inventory)

All paths are relative to `opscore/` (the Go module root). Line counts are approximate upper
bounds — the discipline is "no surprise growth after R92 sign-off."

### 2.1 New package: `internal/tracing/`

| File | LoC | Purpose |
|---|---|---|
| `model.go` | ~60 | `Span` struct (7 fields), `spanKey` private context key, `Refs` helper |
| `span.go` | ~120 | `StartSpan`, `End`, `FromContext`, `WithRef`, `TraceFromContext`, nil-safe guards |
| `traceid.go` | ~50 | `newTraceID()` — crypto/rand-based, opaque, NOT derived from any existing ID |
| `spanid.go` | ~40 | `newSpanID()` — same discipline, unique within trace |
| `ring.go` | ~200 | `TraceRing` type with `Capacity`/`DroppedCount`/`Complete`/`Add`/`QueryByTrace` |
| `model_test.go` | ~80 | R20-6 + R20-9 + nil-safety tests |
| `ring_test.go` | ~180 | bounded eviction, `Truncated` honesty, `DroppedCount` discipline |
| `span_test.go` | ~140 | nil-safe helpers, `WithRef` advisory, `End` idempotency |
| **subtotal** | **~870** | One new peripheral package |

### 2.2 New package: `internal/tracing/peripheral` (AST guard)

| File | LoC | Purpose |
|---|---|---|
| `guard_test.go` | ~30 | `TestNoExecMethod` (forbid `Run`/`Exec`/`Invoke`/`Apply`/`Execute`/`Command`/`Emit`/`Dispatch`/`Rollback`/`Kill`/`Schedule`); `TestNoFrozenImports` (forbid import of `internal/platform`, `internal/governance`, `internal/plugin/runtime`, `internal/plugin/isolation`, `internal/controlplane/hostregistry`) |

This is the **peripheral package guard** convention used by `internal/observability`,
`internal/cluster`, `internal/enterprise`, `internal/governance`. Phase 20 inherits it.

### 2.3 Adapter sink layer: `internal/observability/`

| File | LoC | Change | Reason |
|---|---|---|---|
| `sinks.go` | +25 | Add `ObserveExecutionWithTrace(ev ExecutionView, traceID string)` overload | Population of vestigial `Observation.TraceID` field; **stays at adapter sink layer**, frozen engine untouched |
| `observability_test.go` | +40 | `TestObservationTraceIDPopulated`, `TestObservationTraceIDAdvisory` (R20-10) | Enforce that `TraceID` is set by adapter, not derived from `ExecutionID` |

The frozen execution engine (`internal/core/execution`) does **not** call the new overload — the
adapter (`internal/observability/sinks.go`) does. R20-3 holds.

### 2.4 Management read surface: `internal/management/`

| File | LoC | Change | Reason |
|---|---|---|---|
| `server.go` | +90 | `GET /management/v1/traces` handler, JSON contract per ADR-044 §5 | R20-1 (`8082` + token) |
| `server.go::RoutePatterns()` | +1 | Append `/management/v1/traces` | RoutePatterns 10 → 11 |
| `traces.go` | ~120 | `listTraces(traceID, execID)` — `Refs`-based advisory resolution (R20-10) | R20-10 enforcement |
| `traces_test.go` | ~150 | `TestTracesByTraceID`, `TestTracesByExecutionID`, `TestTracesAdvisoryNotDerived`, `TestTracesTruncatedAlwaysSurfaced`, `TestTracesBadRequest`, `TestTracesNotFound` | Full read surface contract |
| `phase20_test.go` | ~70 | `TestRoutePatternsAreManagementScoped11` (replace `TestRoutePatternsAreManagementScoped10`) | RoutePatterns discipline |

### 2.5 Wiring: `cmd/opscore/main.go`

| File | LoC | Change | Reason |
|---|---|---|---|
| `main.go` | +12 | Construct one `TraceRing` with `DefaultTraceRingCapacity=5000`; pass it to (a) `management` server for read access, (b) `observability.Sinks` via `WithTraceRing(ring)` so spans ingest at `End()` | Composition root only |
| `main.go` wiring tests | +30 | `TestTraceRingWiringReachesManagement`, `TestTraceRingWiringReachesSinks` | Composition root discipline |

### 2.6 Documentation

| File | LoC | Purpose |
|---|---|---|
| `docs/adr/045-phase20-implementation.md` | (this file) | Implementation ADR |
| `internal/tracing/doc.go` | ~25 | Package doc explaining the "trace is a passenger, not a driver" rule |

### 2.7 Total budget

**~1500 LoC** across 1 new peripheral package + 3 existing-package touch-ups. No new dependency,
no frozen-package modification.

---

## 3. The `Span` type — final shape

```go
// internal/tracing/model.go
package tracing

import "time"

type Span struct {
    TraceID      string            // opaque, random, independent identity (R20-6)
    SpanID       string            // unique within TraceID, independent identity (R20-6)
    ParentSpanID string            // empty at trace root
    Start        time.Time
    End          time.Time         // zero until End() called
    Operation    string            // human-readable, NOT a kind/type (no SpanKind)
    Refs         map[string]string // advisory only (R20-6, R20-10)
}

// Duration is a method, not a field, so End() can keep End immutable.
func (s Span) Duration() time.Duration { /* returns End.Sub(Start) */ }

// No methods that block. No methods that touch I/O. No methods that take a context.
```

**Mutation tests** (each verified to fail when the corresponding mutation is applied):

| Mutation | Test that catches it |
|---|---|
| Replace `crypto/rand` with `ExecutionID` derived bytes | `TestTraceIDIsNotExecutionID` |
| Add `Sampler` interface field | `TestNoSamplingPolicy` (compile-time AST check) |
| Make `End()` block on a `sync.Mutex` of unbounded lock | `TestSpanEndIsNonBlocking` |
| Add `Attributes`, `SpanKind`, `Status`, `Events` fields | `TestSpanHasExactlySevenFields` (reflection-based) |

---

## 4. The `TraceRing` type — final shape

```go
// internal/tracing/ring.go
package tracing

const DefaultTraceRingCapacity = 5000

type TraceRing struct { /* bounded ring + DroppedCount + Complete() */ }

func NewTraceRing(capacity int) *TraceRing
func (r *TraceRing) Capacity() int
func (r *TraceRing) DroppedCount() int64
func (r *TraceRing) Complete() bool                       // false iff DroppedCount > 0
func (r *TraceRing) Add(s Span)                            // non-blocking; counted eviction
func (r *TraceRing) QueryByTrace(traceID string) ([]Span, bool) // (spans, truncated)
```

The ring is **deliberately separate** from `observability.Collector` — merging them would
require touching the existing Collector contract (R20-3 frozen-package boundary). The
discipline (`Capacity`, `DroppedCount`, `Complete`, `Truncated`) is the **same** discipline
(Phase 18 lesson), but the storage is independent.

**R20-10 enforcement** is in `QueryByTrace`: it returns `(spans, truncated bool)`. The
management read surface MUST attach `truncated` to the response whenever `truncated` is true.
There is no code path that drops the flag.

---

## 5. Read surface — final contract

```
GET /management/v1/traces?execution_id=X    # advisory lookup (R20-10)
GET /management/v1/traces?trace_id=Y        # direct lookup

→ 200 { trace_id: string, spans: [...], truncated: bool }
    # spans include Refs in the JSON form: { "kind": "execution", "id": "..." }
    # truncated is always present; true iff ring eviction was observed
→ 400 if neither query param supplied
→ 401 if token missing (existing management gate)
→ 404 if no matching trace
    # CRITICAL (R20-10): 404 is the ONLY response when execution_id lookup
    # cannot find a trace. Never return a synthesized TraceID from ExecutionID.
→ 503 evidence_unavailable if TraceRing is nil
```

The handler is registered **only** in `internal/management/server.go`. `internal/harness/server.go`
already routes all `management.RoutePatterns()` entries, so no harness change beyond the route
list growing 10 → 11.

---

## 6. Test plan — property tests + mutation tests

### 6.1 Property tests (each mutation-verified)

| # | Test | Enforces |
|---|---|---|
| P-1 | `TestTraceIsNilSafe` | All `tracing.*` helpers panic-free on missing-span contexts |
| P-2 | `TestTraceIDIsNotExecutionID` | `TraceID` randomly minted, NOT derived from any existing ID |
| P-3 | `TestRefsAreNotIdentity` | zero-ref span is valid (refs advisory) |
| P-4 | `TestNoSamplingPolicy` | No `Sampler`, no `Sample()`, no ratio knob in public API (AST) |
| P-5 | `TestRingEvictionIsCounted` | `DroppedCount()` increments; `Complete()` returns false |
| P-6 | `TestMissingTraceContextNoFailure` | Execution never fails on missing trace ctx (integration) |
| P-7 | `TestTraceRingTruncatedFlag` | `Truncated` always surfaced on partial result |
| P-8 | `TestFrozenPackagesUnchanged` | `git diff` of frozen packages empty (CI gate) |
| P-9 | `TestRoutePatternsAreManagementScoped11` | 10 → 11, all management-scoped |
| P-10 | `TestNewDepRejected` | `go.mod` diff vs R89 baseline = 0 (CI gate) |
| **P-11** | `TestTracesAdvisoryNotDerived` | `execution_id` lookup with NO matching `Refs` returns 404, NEVER 200 with synthetic `TraceID` (R20-10) |
| **P-12** | `TestTruncatedNeverDropped` | Successful 200 response that experienced eviction MUST have `truncated: true` (R20-10) |

### 6.2 Mutation tests (each red→green discipline)

| # | Mutation | Catching test |
|---|---|---|
| M-1 | Make `StartSpan` panic when ctx has no span | P-1 |
| M-2 | Derive `TraceID` from `ExecutionID` (hash) | P-2 |
| M-3 | Treat first `Ref` as identity | P-3 + P-11 |
| M-4 | Add `Sample(ratio float64)` function | P-4 |
| M-5 | Make ring eviction silent (no `DroppedCount` increment) | P-5 |
| M-6 | Block `End()` on channel send | P-6 |
| M-7 | Drop `Truncated` flag from response | P-7 + P-12 |
| M-8 | Touch any frozen package | P-8 |
| M-9 | Skip the route registration | P-9 |
| M-10 | Add `go.opentelemetry.io/otel` to `go.mod` | P-10 |
| M-11 | Synthesize `TraceID = ExecutionID` in management handler | P-11 |

---

## 7. Risk analysis (R91 mitigations)

### 7.1 Risk: `execution_id → trace_id` becomes implicit identity

**R91 explicitly flagged this.** The risk is that someone "helpfully" implements
`tracesByExecutionID` by hashing the `ExecutionID` to mint a `TraceID` — which would silently
violate R20-6 (the trace would no longer have an independent identity) and would resurrect
the Phase 18 false-clean class (404 would never fire because the hash would always produce
a `TraceID`).

**Mitigation:**
1. P-11 (`TestTracesAdvisoryNotDerived`) is a **black-box** test: it feeds a synthetic
   `ExecutionID` with **zero** `Refs` in the ring and asserts 404.
2. The handler code path explicitly documents "advisory resolution" at the top of the
   function — review-time discoverable.
3. M-11 mutation (synthesize `TraceID = ExecutionID`) MUST turn P-11 red.

### 7.2 Risk: `truncated` flag dropped from response

**R91 explicitly flagged this.** The risk is that ring eviction produces a "looks complete"
200 — masking the truth.

**Mitigation:**
1. P-12 (`TestTruncatedNeverDropped`) is a **black-box** test: it pre-fills the ring to
   trigger eviction, queries for the trace, and asserts `truncated: true` is in the JSON
   response.
2. The handler code path uses a single `truncated := ring.Complete() == false` variable
   that MUST be threaded into every successful response builder.
3. M-7 mutation (drop the flag) MUST turn P-12 red.

### 7.3 Risk: frozen-package violation sneaks through

**Standing risk.** R20-3 has been holding since Phase 9.1. The `internal/tracing/peripheral`
guard package adds `TestNoFrozenImports` to lock this in for the new package itself.

---

## 8. Wiring order (commit sequence)

This is the order files are written and committed within R92 sign-off. The discipline is
"additive, observable, then contract":

1. `internal/tracing/model.go` + `internal/tracing/spanid.go` + `internal/tracing/traceid.go` —
   pure data types, no I/O, zero dependencies on existing packages.
2. `internal/tracing/span.go` — context-key helpers, all nil-safe.
3. `internal/tracing/ring.go` — bounded ring, Phase 18 discipline.
4. `internal/tracing/{model,span,ring}_test.go` — P-1..P-7, M-1..M-7.
5. `internal/tracing/peripheral/guard_test.go` — P-8 (frozen-package guard for self).
6. `internal/observability/sinks.go` — `ObserveExecutionWithTrace` overload (+25 LoC).
7. `internal/observability/observability_test.go` — P-2 adapter-side check.
8. `internal/management/traces.go` — handler with R20-10 baked in.
9. `internal/management/traces_test.go` + `internal/management/phase20_test.go` — P-9, P-11, P-12.
10. `internal/management/server.go` — `RoutePatterns()` 10 → 11 + handler registration.
11. `cmd/opscore/main.go` — wire `TraceRing` to both `management` and `observability.Sinks`.
12. `internal/tracing/doc.go` — package doc.

Each step is a `git commit` (with quality-gate bypass only after manual `go build` / `go vet` /
`go test -p 1 ./...` verification — same discipline as Phase 17.2).

---

## 9. Migration / shim plan

**There is no migration.** Phase 20 is purely additive:
- No existing code path changes behavior (R20-4).
- `Observation.TraceID` becomes *populated* but the field already exists (vestigial since
  ADR-015). No JSON contract change for existing read surfaces.
- `RoutePatterns()` grows; no route is removed or renamed.
- `external/v1` untouched.

The only "visible change" is that `GET /management/v1/traces` now returns data when spans
exist. Before Phase 20 it would return 404 (no trace storage). After Phase 20 it returns 200
for any trace with non-empty spans.

---

## 10. Sign-off record

### 10.1 Scope (ADR-043, R90)

**Verdict B: ACCEPT WITH MODIFICATIONS.** Two modifications frozen as R20-6 (rewrite) and
R20-9 (new). Critical guard from R90 verbatim.

### 10.2 Architecture (ADR-044, R91)

**Verdict A: ACCEPT.** One implementation constraint frozen as **R20-10** (this ADR).

### 10.3 Implementation (ADR-045, R92)

**Verdict A: ACCEPT.** R92 carried one implementation-level invariant — "a trace must never
be considered complete merely because the queried trace has not been found" — which is
already consistent with P-12/R20-10, so **no ADR modification was required**.

**Implementation is authorized.** Proceed with the §2 file inventory and §6 mutation-test plan.
No scope expansion, sampling, OTel/exporter integration, mutation, or frozen-package modification
is authorized.

Full text of this ADR was the R92 prompt. 12 property tests + 11 mutation tests. Implementation
commits begin at `internal/tracing/{model,spanid,traceid}.go` per the §8 wiring order.
ADR-021 three-tier discipline has been held — implementation was the third and final gate
before code touches the repo.