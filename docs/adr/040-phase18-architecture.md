# ADR-040 — Phase 18: Evidence Integrity (Architecture)

- **Status**: Proposed (Round 85 — seeking **Architecture sign-off**; no code is written until signed)
- **Date**: 2026-08-10
- **Companion to**: ADR-039 (Phase 18 Scope, **SIGNED R84-A**), ADR-037/038 (Phase 17.3, CLOSED),
  ADR-021 (Architecture Baseline / three-tier discipline, frozen), ADR-015 (Observability
  Architecture, CLOSED)
- **Author**: OpsCore Plugin Runtime Workstream
- **Theme owner**: Phase 18 — the read side of the audit contract must be incapable of a false-clean
  or a silent truncation.

---

## 0. Abstract

ADR-039 was **ACCEPTED in Round 84 (verdict A)**; the Round-83 classification of OQ-17.3-2 was
formally revised (performance → correctness). This ADR resolves the four questions Round 84 required
before any code is written (ADR-039 §7.2):

1. the audit query model (shape, filtering, `limit`, ordering, empty-vs-unavailable);
2. the reconciliation result contract (`verified` / `truncated` / `unknown`, never collapsed);
3. the collector contract (capacity, eviction, dropped accounting, **completeness**);
4. how all of the above is enforced **mechanically**, not by convention.

One sentence governs every decision below:

> **Absence of a finding is only meaningful when the scope of the search is stated.**

---

## 1. Design invariants (inherited, restated)

| Law | Meaning here |
|---|---|
| R18-0 | `external/v1` untouched. All changes land on `:8082` + `internal/storage` + `internal/observability`. |
| R18-1 | No success-shaped empty result on a failed read. |
| R18-2 | Every capped read returns window metadata. |
| R18-3 | No new mutation. `ReconcileForward` remains an uninvoked seam. |
| R18-4 | No scheduler/worker/controller. On-demand + one startup pass only. |
| R18-5 | Frozen packages zero-diff. |
| R18-6 | Additive storage only; index-only migration; never the v1 baseline. |
| R18-7 | No new dependency. |
| R18-8 | Audit append-only. No retention, purge, compaction. |

---

## 2. Blast-radius decision (why signatures stay stable)

`internal/platformview` and `internal/correlation` are **frozen** and consume observability through
locally-declared interfaces (`QueryObservations`, `QueryObservationRefs`) that are satisfied by
**adapters in `internal/harness`**, not by `Collector` directly.

Decision: **`Collector.Query`, `Collector.Counter(s)`, `Collector.Count` keep their exact current
signatures.** Completeness is exposed through *additive* accessors. Consequences: zero adapter churn,
zero risk to the frozen interfaces, and R18-5 is satisfied structurally rather than by review.

Symmetrically, `AuditStore.Append / List / ListByOperation / ListByCorrelation` are **unchanged**;
Phase 18 adds exactly one method.

---

## 3. Architecture decisions

### 3.1 Audit query model (S-3, R18-6) — `internal/storage`

```go
// AuditQuery is a predicate for AuditStore.Query. An empty field is a wildcard.
// Filtering happens IN THE STORE, so Limit means "maximum rows RETURNED" —
// the ordinary meaning of the word, and the one Phase 17.3 violated.
type AuditQuery struct {
	Target        string // AuditEvent.Target (policy id); "" = any
	Result        string // "success" | "failure" | "intent"; "" = any
	Action        string // frozen policy.* vocabulary; "" = any
	CorrelationID string // "" = any
	Limit         int    // <=0 → DefaultAuditQueryLimit; clamped to MaxAuditQueryLimit
}

// AuditPage is a query result plus the window metadata that makes a negative
// result interpretable (R18-2).
type AuditPage struct {
	Events    []AuditEvent `json:"events"`
	Limit     int          `json:"limit"`     // effective limit actually applied
	Truncated bool         `json:"truncated"` // more matching rows exist beyond this page
}

// Query is ADDITIVE. Existing methods and all their callers are untouched.
Query(q AuditQuery) (AuditPage, error)
```

**Filtering semantics** — conjunctive: every non-empty field must match, exact equality (no LIKE, no
prefix, no regex; matching stays boring and predictable).

**`limit` semantics** — rows *returned*. `DefaultAuditQueryLimit = 100`, `MaxAuditQueryLimit = 1000`;
a request above the max is clamped **and the clamp is visible** via `AuditPage.Limit`, never silent.

**Ordering guarantee** — `ORDER BY id DESC` (newest first), matching `List`. `id` is a monotonic
INTEGER PRIMARY KEY, so the order is total and stable even for same-timestamp rows.

**Truncation detection** — the store issues `LIMIT n+1`. If `n+1` rows come back it returns the first
`n` with `Truncated: true`. Exact, single round-trip, no `COUNT(*)`.

**Empty vs unavailable** — the return is `(AuditPage, error)`:
`AuditPage{Events: []}` + `nil` error means *"searched, found nothing"*; a non-nil error means
*"could not search"*. **The two are never collapsed.** This is the whole phase in one signature.

**SQL construction** — a pure, unit-testable builder:

```go
func buildAuditQuery(q AuditQuery) (sql string, args []any)
```

Every predicate is a bound `?` parameter; no value is ever concatenated into the SQL text. The builder
being pure is what makes that assertable in a test rather than reviewable by eye.

**Index migration (v5, index-only)** — a new migration that creates indexes and nothing else:

```sql
CREATE INDEX IF NOT EXISTS idx_audit_target ON audit_events(target);
CREATE INDEX IF NOT EXISTS idx_audit_result ON audit_events(result);
```

No column, no table, no data rewrite, no baseline edit (R18-6; and per the R83 §1.1 ruling an index
lives in a migration where its column already exists). `idx_audit_corr` from v4 already covers
`correlation_id`.

**In-memory store** — `memAuditStore.Query` applies the identical predicate/limit/truncation rules so
the two implementations are behaviourally interchangeable; a shared conformance test asserts it.

---

### 3.2 Reconciliation result contract (S-1 + S-2) — `internal/management`

```go
// ScanReport is the reconciliation envelope. Status makes failure inexpressible
// as health; Window makes an empty Entries list interpretable.
type ScanReport struct {
	Status  string        `json:"status"` // scanVerified | scanTruncated | scanUnknown
	Window  ScanWindow    `json:"window"`
	Entries []ReportEntry `json:"entries"`
}

type ScanWindow struct {
	Scanned      int    `json:"scanned"`               // audit rows actually examined
	Cap          int    `json:"cap"`                   // ceiling applied
	Truncated    bool   `json:"truncated"`             // matching rows exist beyond the window
	Unexaminable int    `json:"unexaminable"`          // rows whose evidence could not be read
	Reason       string `json:"reason,omitempty"`      // set only when Status == scanUnknown
}

const (
	scanVerified  = "verified"  // complete window read successfully
	scanTruncated = "truncated" // read OK, but the window hit the cap
	scanUnknown   = "unknown"   // the read itself failed — Entries carries NO information
)
```

**Interpretation rules (normative):**

| Status | `Entries` empty means |
|---|---|
| `verified` | there are no orphaned intents. A real answer. |
| `truncated` | there are none **in the newest `Scanned` rows**. Says nothing about older rows. |
| `unknown` | **nothing.** `Entries` MUST be empty and MUST NOT be read as evidence. |

**Signature** — `Scan(ctx) (ScanReport, error)`. The error is returned *in addition to*
`Status: scanUnknown`, so neither a structured consumer nor a Go caller can miss it.

**Per-row evidence failure** — the current `continue`-on-error (an intent silently vanishing) is
replaced: the entry is emitted with entry-level status `unexaminable` and `Window.Unexaminable`
is incremented. The row is **reported, never dropped**. Report-level `Status` stays `verified`
(the scan itself completed) — per-item truth belongs on the item.

```go
const reportUnexaminable = "unexaminable" // joins closed / unresolved / no_match
```

**HTTP mapping (one rule, three call sites — ADR-039 §3.3):**

| Condition | Status | Code |
|---|---|---|
| scan/query succeeded (incl. empty, incl. truncated) | `200` | — |
| underlying audit read failed | **`503`** | `evidence_unavailable` |

`503`, not `500`: the request was well-formed and the server is not broken — the *answer is currently
unknowable*. `503` tells an operator "retry, do not conclude"; `500` invites "the tool is buggy,
ignore it". `handleListAudit`'s present `500 codeInternal` on store failure moves to the same
`503 evidence_unavailable`, so both surfaces speak one language.

`GET /management/v1/audit` returns the `AuditPage` envelope (`{events, limit, truncated}`) instead of
a bare array — the window metadata is the point, and a bare array cannot carry it.

**Startup pass** — `ScanAtStartup` logs by status and never prints a clean-looking line for a failed
scan:

```
verified  : "reconciliation scan verified: N entries, M non-closed"
truncated : "reconciliation scan TRUNCATED (cap=C): N entries, M non-closed — older rows NOT examined"
unknown   : "reconciliation scan UNKNOWN: audit unreadable: <err> — no conclusion may be drawn"
```

`scanCap` stays `1000`, unchanged. Phase 18 does not raise the ceiling — it makes the ceiling
**visible**. Raising it is a capacity decision, not an integrity one.

---

### 3.3 Collector contract (S-4 + R84 §7.1 clarification) — `internal/observability`

```go
type Collector struct {
	mu       sync.RWMutex
	obs      []Observation // bounded ring; len(obs) <= capacity
	head     int           // ring head (oldest)
	capacity int
	dropped  int64
	counters map[string]int64
}

const DefaultCollectorCapacity = 10000

func NewCollector() *Collector                    // DefaultCollectorCapacity
func NewCollectorWithCapacity(n int) *Collector   // n <= 0 → default

// Completeness contract (R84 §7.1) — additive, non-breaking.
func (c *Collector) Capacity() int      // configured ceiling
func (c *Collector) DroppedCount() int64 // observations evicted since start
func (c *Collector) Complete() bool     // DroppedCount() == 0
```

**Eviction** — FIFO: at capacity the oldest observation is overwritten and `dropped++`. Ring buffer,
so ingest stays O(1) and allocation-free after warm-up.

**The aggregate/sample split (the important part).** Counters are bumped at ingest and are **never
re-derived from `obs`**, so eviction does not corrupt them:

- `Counter()` / `Counters()` — **exact for all time**, regardless of eviction.
- `Query()` / `Count()` — a **bounded window** over the most recent `capacity` observations.

This is stated in the package doc, because an undocumented split is exactly the silent false-clean
R84 §7.1 warned about: a consumer must be able to tell "the aggregate is complete" from "the sample
is a window".

**Signatures unchanged** — `Query`, `Counter`, `Counters`, `Count` are untouched (§2). Completeness
travels on the collector, not in every result, so no frozen consumer sees a shape change.

**Ordering** — `Query` continues to return ingest order (oldest → newest) within the retained window.

---

### 3.4 Mechanical isolation guarantees (R18-1/R18-2/R18-3/R18-5)

Every law gets a test that fails when the law is broken. Each is mutation-verified (break it → red →
restore) before the fix lands.

**AST guards** (extending the existing `guards_test.go` pattern):

- `TestNoErrorSwallowInEvidencePath` — parses `reconcile.go` + the evidence handlers and rejects any
  `if err != nil { return nil }` or `if err != nil { continue }`. This is the F-1/F-2 defect class
  expressed as syntax, so it cannot silently reappear.
- `TestReconciliationDoesNotMutate` — **retained unchanged** from Phase 17.3 (`Append`/`Save`/
  `Activate`/`Deactivate`/`Archive`/`CompareAndSave`/`CompareAndTransition`/`NextRevision` forbidden
  in `reconcile.go`). Phase 18 must not weaken it.
- `TestAuditQueryUsesBoundParameters` — asserts `buildAuditQuery` emits `?` placeholders and that no
  argument value appears inside the SQL string.

**Behavioural mutation tests:**

| Test | Law |
|---|---|
| `TestScanStoreFailureIsUnknown` | failing store → `Status=unknown` + error; **never** `verified` |
| `TestReconciliationEndpointFailureIs503` | that failure → `503 evidence_unavailable`, never `200 []` |
| `TestScanTruncatedIsFlagged` | `cap+1` rows → `Status=truncated`, `Window.Truncated=true` |
| `TestScanVerifiedOnCompleteWindow` | under-cap clean trail → `Status=verified` |
| `TestScanUnexaminableRowIsReportedNotDropped` | per-row read failure → entry emitted, counter bumped |
| `TestAuditQueryFiltersBeforeLimit` | **the decisive one:** policy X appears only in the OLDEST rows; `?policy=X&limit=10` returns X's events, not `[]` |
| `TestAuditQueryTruncationFlag` | more matches than limit → `truncated=true` |
| `TestAuditQueryLimitClampIsVisible` | `limit=99999` → clamped, `AuditPage.Limit` reports the clamp |
| `TestAuditEndpointFailureIs503` | store failure → `503 evidence_unavailable` |
| `TestAuditStoreConformance` | sqlite and memory stores agree on predicate/limit/truncation |
| `TestCollectorEvictsFIFOAndCountsDropped` | over-capacity ingest → oldest gone, `DroppedCount()>0` |
| `TestCollectorCountersExactAfterEviction` | counters unaffected by eviction |
| `TestCollectorCompleteReflectsDrops` | `Complete()` true before any drop, false after |

**Frozen-package zero-diff** — `git diff --stat` over `platform`, `governance`,
`plugin/{runtime,isolation}`, `controlplane/hostregistry`, `external`, `governancepolicy`,
`platformview` MUST be empty (R18-5), asserted at review time as in Phase 17.3.

**Route surface** — `RoutePatterns()` stays at **7**. Phase 18 adds no route; it changes two response
bodies and one status-code mapping. `TestRoutePatternsAreManagementScoped` and the external-mux
isolation assertion are unchanged.

---

## 4. What Phase 18 deliberately does NOT do

- Does not raise `scanCap` (visibility, not capacity).
- Does not add `/metrics`, Prometheus text format, or OTel (R18-7; deferred to Phase 19).
- Does not add pagination cursors — `truncated` is a *signal*, not a paging contract. Cursored paging
  is a Phase 19 API decision.
- Does not touch `ReconcileForward` (R18-3, OQ-17.3-1 stays closed).
- Does not add retention/purge/compaction (R18-8).
- Does not persist observability (the collector stays in-memory by design, ADR-015).

---

## 5. Compatibility notes

- `AuditStore` gains one method. Every existing caller of `List`/`ListByOperation`/`ListByCorrelation`
  is unchanged. Any third implementation of the interface (test doubles included) must add `Query` —
  the compiler enumerates them exhaustively, so nothing is missed silently.
- `GET /management/v1/audit` **changes response shape** from a bare array to
  `{events, limit, truncated}`. Accepted, and deliberate: a bare array is structurally incapable of
  carrying the window metadata R18-2 requires. The surface is one phase old, `:8082`-only,
  token-gated, and has no external consumer contract (R18-0 keeps `external/v1` untouched).
- `GET /management/v1/reconciliation` changes from a bare array to `{status, window, entries}` for the
  same reason.

---

## 6. Phase 18 roadmap (authorization chain)

| Step | Deliverable | Status |
|---|---|---|
| 18-scope | ADR-039 Phase 18 Scope | **SIGNED (R84-A)** |
| 18-arch | ADR-040 Phase 18 Architecture (this ADR) | **proposed — R85 (seeking sign-off)** |
| 18-impl | Implementation + mutation tests | blocked — after 18-arch sign-off |

---

## 7. Sign-off placeholder (Round 85)

| Item | Verdict | Round |
|---|---|---|
| §3.1 Audit query model (`AuditQuery`/`AuditPage`/`Query`, `LIMIT n+1`, v5 index-only) | ⬜ | 85 |
| §3.2 Reconciliation contract (`verified`/`truncated`/`unknown` + `unexaminable` entries) | ⬜ | 85 |
| §3.2 `503 evidence_unavailable` for both surfaces | ⬜ | 85 |
| §3.3 Collector contract (ring + `Capacity`/`DroppedCount`/`Complete`, aggregate/sample split) | ⬜ | 85 |
| §3.4 Mechanical guards incl. `TestNoErrorSwallowInEvidencePath` | ⬜ | 85 |
| §5 Response-shape change on the two `:8082` GETs | ⬜ | 85 |
| §2 Signature-stability decision (frozen zero-diff by construction) | ⬜ | 85 |

---

## 8. ADR-040 acceptance gate

ADR-040 is signed only when **all** of the following are settled:

- [x] `AuditQuery` shape, filtering semantics, `limit` semantics, ordering guarantee, truncation
      detection, empty-vs-unavailable distinction (§3.1)
- [x] Index-only v5 migration; no baseline edit; no column/table change (§3.1)
- [x] Three-state reconciliation contract, never collapsed; per-row `unexaminable` (§3.2)
- [x] One HTTP failure rule across all three evidence call sites (§3.2)
- [x] Collector capacity/eviction/dropped/completeness + aggregate-vs-sample split (§3.3)
- [x] Mechanical guards for each law, each mutation-verified (§3.4)
- [x] Frozen-package zero-diff argued structurally, not by review (§2)

No implementation begins until this gate is signed.
