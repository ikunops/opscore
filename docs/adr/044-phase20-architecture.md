# ADR-044 — Phase 20: Causal Tracing (Architecture)

- **Status**: **DRAFT — seeking Round 91 sign-off**. ADR-043 (Scope) is **ACCEPTED WITH
  MODIFICATIONS** at R90 (verdict B). This ADR freezes those modifications and defines the
  architecture. **No implementation until R91 sign-off is granted.**
- **Date**: 2026-08-10
- **Companion to**: ADR-043 (Phase 20 Scope, ACCEPTED WITH MODIFICATIONS at R90),
  ADR-041/042 (Phase 19, CLOSED), ADR-039/040 (Phase 18, CLOSED),
  ADR-022 (Phase 10 Correlation, CLOSED), ADR-015 (Observability Architecture, CLOSED),
  ADR-021 (three-tier discipline, frozen)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 20 architecture — the concrete shape of the causal tracing layer
  that ADR-043 scoped.

---

## 0. Abstract

ADR-043 proposed Phase 20 = *Causal Tracing* and was accepted at R90 with two modifications:
**R20-6 rewrite** (TraceID is a trace identity, not an `ExecutionID` alias) and **R20-9
addition** (no implicit sampling). This ADR freezes those modifications and specifies the
concrete architecture: the `Span` struct, the context-key strategy, the ring integration,
the trace-tree reconstruction format, the `TraceID` population path, and the management
read-surface contract. The hand-rolled model is deliberately small — exactly the fields R90
listed (`TraceID`, `SpanID`, `ParentSpanID`, `Start`, `End`, `Duration`, `Refs`) — and nothing
more. No `SpanKind`, no exporters, no OTLP baggage, no vendor propagation, no collector
protocols. **Do not implement a pirate OTel clone.**

> Phase 17: the system cannot lie about what it *did*.
> Phase 18: the system cannot lie about what it *sees*.
> Phase 19: the system lets you *read what it sees* — at scale.
> Phase 20: the system lets you *follow what it did* — as a chain.

---

## 1. Scope modifications frozen at R90 (binding constraints)

These are the two modifications R90 imposed on ADR-043. They are **frozen** here and MUST be
reflected verbatim in any Phase 20 implementation.

### 1.1 R20-6 — Explicit trace identity

> `TraceID` is a **trace identity**, not an `ExecutionID` alias.
> `SpanID` is independently unique within the trace.
> Existing IDs (`ExecutionID`, `RequestID`, `CorrelationID`, `CapabilityID`, `PluginID`,
> `AuditID`) are attached as **Refs**, never as identity substitutes.
> A trace may *reference* an execution, but an execution does not *define* the trace identity.
> ID generation must not introduce another externally visible identity system.

**Architectural consequence:** The trace model mints its own `TraceID` (a random opaque
handle, like `ObsID`) at the root of each causal chain. It does NOT alias any existing ID.
`ExecutionID` / `RequestID` / etc. are attached as `Refs` when known, but the `TraceID` exists
independently. This means a trace can exist before an `ExecutionID` is assigned (e.g. at the
entry boundary) and can survive across subsystem boundaries that don't share an execution
context.

### 1.2 R20-9 — No implicit sampling

> For Phase 20: *eligible span = captured*, until bounded storage eviction occurs.
> If sampling is introduced later, it requires an explicit Scope + Architecture ADR and must
> define how sampled-out spans are distinguished from nonexistent spans.

**Architectural consequence:** There is no sampling policy, no sampling ratio, no sampler
interface. Every span that `End()`s is captured (subject only to bounded-ring eviction). The
ring's `Truncated` flag is the **only** reason a span can be missing from a query response;
"sampled out" is not a possible state in Phase 20. If Phase 21+ ever introduces sampling, it
must add a new field to the response (e.g. `Sampled: bool`) so consumers can distinguish
"never happened" from "happened but not captured".

### 1.3 Critical guard (R90 verbatim)

> **Tracing must never alter execution behavior.**
> **A missing trace context must not cause execution failure.**

**Architectural consequence:** All `trace.*` helpers are **nil-safe and panic-free**. If no
span is in the context, `StartSpan` returns a no-op span (or `nil` — see §3.2). `End()` on a
no-op is a no-op. The execution path cannot tell whether tracing is active. This is enforced
by a property test in §6.

---

## 2. Positioning — composition, not instrumentation

The Phase 20 trace model follows the same composition-over-instrumentation pattern that
ADR-015 established for Observability and ADR-022 established for Correlation:

```
Frozen systems (Execution / Plugin / Isolation / Audit)
   │ emit events through existing buses
   │ accept context.Context as they always have
   ▼
trace package (new, peripheral — internal/tracing)
   │
   ▼
internal/observability collector (existing) — extended
   │
   ▼
management surface :8082 (existing) — new GET /traces
```

**Key properties (inherited from ADR-015):**

- **Peripheral package.** `internal/tracing` depends on the frozen systems' context-passing
  convention; the frozen systems never import it. An AST guard forbids `internal/tracing`
  from importing `internal/plugin/runtime` / `core` / `builtin` in a way that lets it
  participate in execution.
- **Composition, not intrusion.** The trace model attaches to execution via the **existing**
  `context.Context` parameter. No frozen type gets a new field, method, or import.
- **Existing IDs as Refs, not identity.** Per R20-6 frozen: existing IDs are attached as
  references; the trace identity is minted by the trace model itself.

---

## 3. Architecture — components and contracts

### 3.1 The `Span` struct (frozen shape)

```go
// Span is the unit of causal tracing in Phase 20. The shape is deliberately small
// (per R90: no SpanKind, no Attributes map, no Status codes, no Events).
type Span struct {
    // TraceID is the trace identity, minted by the trace model (R20-6).
    // It is NOT an alias for ExecutionID or any other existing ID.
    TraceID string

    // SpanID is independently unique within the trace.
    SpanID string

    // ParentSpanID is the empty string for root spans; otherwise the SpanID of
    // the immediately-causing span.
    ParentSpanID string

    // Start is when the span began (monotonic clock, time.Now() at StartSpan).
    Start time.Time

    // End is when End() was called. Zero before End().
    End time.Time

    // Duration is End.Sub(Start), or Start.Sub(time.Now()) if not yet ended.
    // Exposed as a method, not a field, to avoid stale cached values.
    Duration time.Duration

    // Operation is a short string describing what the span represents
    // ("request.handle", "execution.run", "plugin.load", "sandbox.decide").
    Operation string

    // Refs carries existing IDs attached to this span. Per R20-6, these are
    // references, not identity. May be empty for a freshly-rooted span that
    // has not yet observed any subsystem event.
    Refs map[string]string
}
```

**No `SpanKind`, no `Attributes map[string]any`, no `Status code`, no `Events []Event`.** These
OTel-isms are explicitly out of scope (R90: "Do not implement a pirate OTel clone."). If a
span needs to carry a duration, a status, or a label, the `Operation` string + `Refs` map is
the contract; subsystem-specific signals belong on the `Observation` already produced by
Phase 18's collector.

### 3.2 The trace package surface

```go
package tracing

// StartSpan begins a new span as a child of the span carried in ctx. If ctx has no
// span, the new span is a root (ParentSpanID == ""). Returns the new span AND a
// derived context that carries it.
//
// Nil-safe: returns (nil, ctx) if tracing is disabled or the trace model is
// misconfigured. The execution path cannot distinguish "no span" from "span".
func StartSpan(ctx context.Context, operation string) (Span, context.Context)

// End finalises the span. Idempotent: calling End twice is a no-op.
//
// Nil-safe: End on a no-op span (returned by StartSpan when disabled) is a no-op.
// Critical: End NEVER panics, even on a zero-value Span.
func (s *Span) End()

// FromContext retrieves the current span from ctx, if any. Returns (zero, false)
// when no span is present. Zero-value Span is safe to End().
func FromContext(ctx context.Context) (Span, bool)

// WithRef attaches an existing-ID reference (ExecutionID, RequestID, etc.) to the
// span carried in ctx. Returns a derived context. No-op if no span is present.
// Refs are advisory — a span may have zero refs (R20-6: refs are not identity).
func WithRef(ctx context.Context, kind, id string) context.Context

// TraceFromContext returns the TraceID of the current span, or "" if no span.
// Convenience accessor; equivalent to s, _ := FromContext(ctx); s.TraceID.
func TraceFromContext(ctx context.Context) string
```

**Nil-safety is load-bearing** (R90 critical guard). Every method must be safe to call on a
nil span or with a context that carries no span. This is enforced by `TestTraceIsNilSafe`
(§6).

### 3.3 The context key strategy

```go
package tracing

// spanKey is a private type used as a context key. Private types prevent external
// packages from reading or writing the span value directly — they must go through
// StartSpan / FromContext / WithRef. This is the standard Go idiom for context
// values that should not leak across package boundaries.
type spanKey struct{}

// Span lifecycle:
//   StartSpan → context.WithValue(ctx, spanKey{}, span) → returns derived ctx
//   End       → no context mutation; span.End stamps End and is GC'd with ctx
```

**No global state, no goroutine-local, no `sync.Once` singleton.** (R20-5.) The span lives
only where its context goes. `FromContext` is O(1) and safe for concurrent use (the
context value is immutable for the lifetime of the derived context).

### 3.4 The ring integration (S-3)

The trace ring is **not** a new data structure; it is a **separate ring** from the existing
`observability.Collector` ring. Rationale:

- The `Collector` ring stores `Observation` (Phase 18 shape: point-in-time events). Spans
  are intervals. Forcing both into one ring would require a discriminated union shape
  change — a frozen zero-diff violation (R20-3).
- A separate `TraceRing` type keeps the read and write contracts clean: `Collector.Query`
  returns observations ordered by ingest; `TraceRing.Query(traceID)` returns spans ordered
  by start time.

```go
package tracing

// TraceRing is the bounded in-memory store of completed spans. It is a FIFO ring
// identical in discipline to observability.Collector (Phase 18 lessons):
//   - bounded capacity, configured at construction
//   - DroppedCount() counts evicted spans
//   - Complete() returns dropped == 0
//   - absence from ring ≠ absence from history (audit store is the durable ledger)
type TraceRing struct { /* capacity, head, dropped, total, spans */ }

const DefaultTraceRingCapacity = 5000 // 5k spans; smaller than Collector's 10k
                                     // because each span carries more state.

func NewTraceRing() *TraceRing
func NewTraceRingWithCapacity(n int) *TraceRing // n<=0 falls back to default

func (r *TraceRing) Capacity() int
func (r *TraceRing) DroppedCount() int64
func (r *TraceRing) Complete() bool

// Add stores a completed span. Called by Span.End via the package-internal hook.
// If full, the oldest span is evicted and dropped is incremented.
func (r *TraceRing) Add(s Span)

// QueryByTrace returns all spans sharing the given TraceID, ordered by Start.
// Bounded by capacity; the returned bool reports whether the ring was truncated
// (i.e. the trace may have older spans no longer in the ring).
func (r *TraceRing) QueryByTrace(traceID string) (spans []Span, truncated bool)
```

**Composition root wiring:** the `tracing` package exports a package-level `SetDefaultRing(r
*TraceRing)` called by the harness during composition. Until called, the package uses an
internal no-op ring (`&TraceRing{}` with capacity 0 that drops everything silently). This
ensures unit tests can run without wiring a real ring.

### 3.5 The `TraceID` population strategy

The `Observation.TraceID` field (vestigial since ADR-015) is finally populated. The mechanism
is **collector-side**, not subsystem-side: the `Observe*` methods already take the source
event as parameter; we add an optional `traceID string` parameter via a new overload (or via
a `Collector.WithTraceID(traceID string)` builder — see §3.6).

```go
// collector.go (extended)
func (c *Collector) ObserveExecutionWithTrace(ev execution.ExecutionEvent, traceID string) {
    o := Observation{
        Timestamp:   ev.Timestamp,
        Source:      SourceExecution,
        Kind:        KindTrace,
        ExecutionID: ev.ID,
        RequestID:   ev.ID,
        TraceID:     traceID,  // <-- populated
        Status:      string(ev.Status),
        Code:        string(ev.Type),
        Operation:   string(ev.Type),
    }
    c.record(o)
}
```

**The frozen package is NOT modified** (R20-3). The execution engine does NOT call
`ObserveExecutionWithTrace`; the call happens at the **adapter sink** layer (the same layer
that already adapts `execution.EventBus` → `ObserveExecution`). The adapter is in
`internal/observability`, not in `internal/core/execution`.

### 3.6 The management read surface (S-4)

```go
// internal/management/traces.go (new)

// GET /management/v1/traces?execution_id=X  (or ?trace_id=X)
// Returns:
//   200 with { traceID, spans: [...], truncated: bool } on success
//   400 if neither execution_id nor trace_id is supplied
//   404 if no matching trace
//   503 if the trace ring is unavailable (with err.code="evidence_unavailable")
//
// Each span in the response carries the frozen Span fields, JSON-encoded.
type TraceResponse struct {
    TraceID   string         `json:"traceId"`
    Spans     []SpanResponse `json:"spans"`
    Truncated bool           `json:"truncated"`
}

type SpanResponse struct {
    TraceID      string            `json:"traceId"`
    SpanID       string            `json:"spanId"`
    ParentSpanID string            `json:"parentSpanId,omitempty"`
    Operation    string            `json:"operation"`
    StartMicros  int64             `json:"startMicros"`
    EndMicros    int64             `json:"endMicros"`
    DurationMicros int64           `json:"durationMicros"`
    Refs         map[string]string `json:"refs,omitempty"`
}
```

**No new external route, no `external/v1` change.** The route lives on `:8082` only, is
token-gated (existing `OPSCORE_MANAGEMENT_TOKEN` from Phase 17.2), and follows the
Phase 19 metrics/projection pattern.

**RoutePatterns bumps from 10 → 11.** `TestRoutePatternsAreManagementScoped` is updated.

---

## 4. Iron laws (MUST)

All R20-0…R20-9 from ADR-043 carry forward unchanged. The two modifications frozen at R90 are
explicitly restated as architectural MUSTs:

- **R20-6 (rewrite)** — `TraceID` is a trace identity minted by the trace model. It is NOT
  an alias for `ExecutionID`, `RequestID`, or any other existing ID. Existing IDs are
  attached as `Refs` (advisory), never as identity substitutes. A trace may reference an
  execution, but an execution does not define the trace identity. ID generation must not
  introduce another externally visible identity system.

- **R20-9 (addition)** — No implicit sampling. *Eligible span = captured* until bounded
  storage eviction occurs. There is no sampling policy, no sampler interface, no sampling
  ratio. If sampling is introduced in a future phase, it requires its own Scope +
  Architecture ADR and must add a field to `TraceResponse` (e.g. `Sampled: bool`) so
  consumers can distinguish "never happened" from "happened but not captured".

- **Critical guard (R90):** Tracing never alters execution behavior. A missing trace context
  must not cause execution failure. All `tracing.*` helpers are nil-safe and panic-free.
  `Span.End()` on a no-op span is a no-op. `StartSpan` on a context without a span returns
  a no-op span or `nil` — the execution path cannot tell whether tracing is active.

---

## 5. SHOULD (carry-forward from ADR-043)

- Trace JSON shape is versioned `trace/view/v1` (consistent with `correlation/view/v1`,
  `observability/v1`).
- Span duration is measured with `time.Since(start)` — monotonic clock, no wall-clock drift.
- `TraceID` and `SpanID` are 8-byte random hex (matching `ObsID` style — opaque,
  observability-local, not a new external identity system).
- Every iron law gets a mutation test proven to fail before the fix lands.

---

## 6. Property tests (the architectural contract)

These tests encode the R90 frozen constraints as executable specifications.

| Test | Enforces | Mutation expected |
|---|---|---|
| `TestTraceIsNilSafe` | `StartSpan`/`End`/`FromContext`/`WithRef` are nil-safe on a context without a span | remove nil-check in `End()` → test fails |
| `TestTraceIDIsNotExecutionID` | `TraceID` is NOT derived from `ExecutionID`; it is randomly minted | change mint to use `ExecutionID` → test fails |
| `TestRefsAreNotIdentity` | A span with zero `Refs` is valid; `Refs` are advisory | require non-empty Refs → test fails |
| `TestNoSamplingPolicy` | There is no sampler interface in the public API; every eligible span is captured | add a `Sampler` field → compile/test fails |
| `TestRingEvictionIsCounted` | `TraceRing.DroppedCount()` increments on eviction; `Complete()` returns false | silence dropped increment → test fails |
| `TestMissingTraceContextNoFailure` | Calling `tracing.*` on a context without a span never panics or returns an error | panic on nil span → test fails |
| `TestTraceRingTruncatedFlag` | When `Truncated` is true, the response carries the flag; consumers can distinguish | omit Truncated field → test fails |
| `TestFrozenPackagesUnchanged` | `git diff` of `platform`/`governance`/`plugin/{runtime,isolation}`/`controlplane/hostregistry`/`external`/`governancepolicy`/`platformview` is empty | add any import of `internal/tracing` → test fails |
| `TestRoutePatternsBump` | `RoutePatterns()` returns 11 (was 10); all routes are management-scoped | add a new external route → test fails |
| `TestNewDepRejected` | `go.mod` diff against the R89 baseline is zero | add any new require → test fails |

---

## 7. Out of scope (Phase 20) — explicit

Reaffirming from ADR-043 §3.4, with the R90 OTel boundary made explicit:

- **OpenTelemetry SDK / OTLP exporter / vendor propagation** — rejected. The hand-rolled
  model stays small: `TraceID`, `SpanID`, `ParentSpanID`, `Start`, `End`, `Duration`, `Refs`.
  No `SpanKind`, no `Attributes`, no `Status`, no `Events`, no exporter, no collector
  protocol, no baggage. "Do not implement a pirate OTel clone." (R90)
- **Distributed tracing** — OpsCore is single-process. If cluster execution (ADR-016) ever
  becomes distributed, tracing scope expands.
- **Sampling of any kind** — explicit R20-9 freeze. No head-based, no tail-based, no ratio.
- **Dashboards / UI / Grafana** — presentation concern (ADR-015 SHOULD-2).
- **Audit retention / archival** — R20-8: trace ring is in-memory, bounded, never persisted.
- **`external/v1` changes** — R20-0.
- **Modifying frozen packages** — R20-3. The trace model observes via the existing
  `context.Context` and event bus; it does not inject `StartSpan` calls into
  `Execution`/`Manager`/`isolation`.
- **Background trace exporters** — no goroutine that drains traces to an external sink.

---

## 8. Decision requested (Round 91)

**(A) Accept** Phase 20 Architecture (Span struct, context key, ring integration, read
surface, R20-6 rewrite + R20-9 addition + nil-safe guard all frozen) — authorises Phase 20
implementation, no code before this ADR is signed.

**(B) Accept with modification** — e.g. pin `DefaultTraceRingCapacity` to a different value,
or change the JSON field naming, or split the read surface into two routes.

**(C) Reject** — e.g. insist sampling must be in scope (would require R20-9 to fall, and a
defence against the Phase 18 false-clean class), or insist on `SpanKind` for some reason.

**(D) Other.**

Note: the two modifications from R90 are **frozen** — they cannot be silently re-litigated in
this round. The architectural question is whether the *implementation shape* (struct fields,
context key, ring composition, JSON contract, property tests) correctly embodies them.

---

## 9. Phase 20 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 20-scope | ADR-043 Phase 20 Scope | **ACCEPTED WITH MODIFICATIONS** (R90 verdict B) |
| 20-arch | ADR-044 Phase 20 Architecture (this ADR) | **DRAFT — R91 sign-off pending** |
| 20-impl | Implementation + property tests | blocked — after 20-arch sign-off |

Each step authorized only after the previous is signed (ADR-021).

Global ordering (frozen at R84, reaffirmed R89, R90):

```
Phase 17    write correctness / mutation audit integrity      CLOSED
Phase 17.3  reconciliation visibility                         CLOSED
Phase 18    evidence integrity → trustworthy observations     CLOSED
Phase 19    evidence consumers → readable at scale            CLOSED
Phase 20    causal tracing → execution as a chain             ← here (scope R90, arch R91)
```

---

## 10. Sign-off record (Round 91)

| Item | Verdict | Round |
|---|---|---|
| Span struct shape (7 fields, no OTel-isms) | ⏳ pending | 91 |
| Context key strategy (private type, immutable value) | ⏳ pending | 91 |
| Ring integration (separate `TraceRing`, not Collector union) | ⏳ pending | 91 |
| `TraceID` population (collector-side adapter) | ⏳ pending | 91 |
| Read surface (`GET /management/v1/traces`) | ⏳ pending | 91 |
| R20-6 rewrite correctly embodied | ⏳ pending | 91 |
| R20-9 addition correctly embodied | ⏳ pending | 91 |
| Nil-safe tracing (R90 critical guard) | ⏳ pending | 91 |
| Property tests encode the contract | ⏳ pending | 91 |
| Frozen packages unchanged (R20-3) | ⏳ pending | 91 |
| No new dependency (R20-2) | ⏳ pending | 91 |

*Architecture unsigned. Implementation authorized only after R91 approval.*

---

## 11. Carry-forward into ADR-045 / implementation (to be filled at R92)

Anticipated questions for the implementation ADR or implementation review to answer:

- **Composition root** — where exactly is `tracing.SetDefaultRing` called in `cmd/opscore`?
- **Adapter wiring** — which execution-event subscriptions carry the `TraceID` argument;
  is the parameter optional (empty string = no trace context)?
- **JSON field naming** — final naming for `traceId` / `spanId` / `parentSpanId` (camelCase
  vs snake_case; ADR-019 prefers camelCase — confirm).
- **Sample-of-zero edge case** — what does the response look like when the ring is empty
  for a given `execution_id` (404 vs 200 with empty spans)? Recommend 404 to be consistent
  with Phase 18's `evidence_unavailable` discipline.
- **TraceID minting strategy** — package-level `sync/atomic` counter + `crypto/rand` suffix,
  or pure `crypto/rand`? Recommend pure `crypto/rand` for simplicity.
- **Read surface authn** — does the existing `OPSCORE_MANAGEMENT_TOKEN` apply, or does the
  read surface need a separate read-only token? Recommend: reuse existing token for v1;
  split in a future phase if needed.