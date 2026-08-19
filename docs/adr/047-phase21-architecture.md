# ADR-047 — Phase 21: Operational Protection (Architecture)

- **Status**: **ACCEPTED WITH MODIFICATIONS (R94 verdict B)**. Three additional iron laws
  applied per R94 sign-off: R21-8 (truncated breaker evidence rule), R21-9 (protection
  reject is irreversible within the request), R21-10 (RecordOutcome is protection feedback
  only). All four R93 modifications confirmed PASS (R93-①③④ clean pass, R93-② pass with
  R21-8 tightening). → authorises ADR-048 Implementation. ADR-046 (Scope) ACCEPTED WITH
  MODIFICATIONS at R93 (verdict B, commit `b3b5017`). **No implementation until ADR-048
  sign-off (R95).**
- **Date**: 2026-08-13
- **Companion to**: ADR-046 (Phase 21 Scope, ACCEPTED WITH MODIFICATIONS), ADR-045
  (Phase 20 Implementation, ACCEPTED — must land before Phase 21), ADR-039/041
  (Phase 18/19 Evidence, CLOSED), ADR-021 (three-tier discipline, frozen)
- **Author**: OpsCore Plugin Runtime Workstream

---

## 0. Abstract

ADR-046 defined *what* Phase 21 protects against (S-1..S-5) and *why* (evidence-based gap).
This ADR defines *how*: the concrete package surface, type contracts, storage schema,
wiring order, and test plan for `internal/protection`.

**Four R93 binding constraints** (from Scope sign-off):

| ID | Constraint | Where encoded |
|---|---|---|
| R93-① | Timeout = cancellation signal, not guaranteed termination | S-2 architecture, P-5, M-4 |
| R93-② | Breaker distinguishes unavailable evidence from zero failures; fail closed | S-3 architecture, P-8, P-9, M-5, M-6 |
| R93-③ | Single persistence owner; startup load failure is fail-closed | S-4 architecture, P-11, P-12, M-7, M-8 |
| R93-④ | Protection rejects use `protection.*` event vocabulary, not Policy mutation | R21-6 architecture, P-16, P-17, M-10, M-11 |

**Three R94 additional iron laws** (from Architecture sign-off):

| ID | Constraint | Where encoded |
|---|---|---|
| R21-8 | Truncated breaker evidence below threshold → Unknown (not Closed) | S-3 architecture, P-21, M-12 |
| R21-9 | Protection reject is irreversible within the request (audit failure ≠ admission) | R21-6 architecture, P-22, M-13 |
| R21-10 | RecordOutcome is protection feedback only (no Policy/Runtime/Plugin mutation) | Admission API, P-23, M-14 |

---

## 1. Package surface

**Package**: `internal/protection` (new peripheral package, same pattern as Phase 8
`observability`/`cluster`/`enterprise`/`governance`).

**AST guard** (same as Phase 8 packages):
- Forbidden imports: `internal/plugin/runtime`, `internal/plugin/isolation`,
  `internal/controlplane/hostregistry` (frozen packages).
- `TestNoExecMethod`: no `Run`/`Exec`/`Invoke`/`Apply`/`Execute`/`Command`/`Emit`/
  `Dispatch`/`Rollback`/`Kill`/`Schedule` methods (protection is a gate, not a driver).

**Depends on** (read-only):
- `internal/storage` (KillStore persistence, audit reads)
- `internal/core` (Context, CapabilityID types)
- `internal/audit` (protection.* event writer — Phase 18 audit surface)
- `internal/platformview` (read facade for capability metadata — Phase 9.1)

**Does NOT depend on**:
- `internal/plugin/runtime` (frozen — gate sits before executor)
- `internal/governance` (frozen — gate is parallel to, not inside, governance)
- `internal/controlplane/hostregistry` (frozen)

---

## 2. Core types

### 2.1 Gate — the main entry point

```go
// Gate checks all 5 guards in a fixed order.
// It is the sole protection entry point called by the request boundary.
type Gate struct {
    kills   *KillStore           // S-4
    breaker *BreakerSet          // S-3
    sem     *SemaphoreSet        // S-5
    buckets *TokenBucketSet      // S-1
    audit   AuditWriter          // R21-6: protection.* events
    clock   func() time.Time     // injectable for testing
}

// Check evaluates all guards. Returns Admission if all pass, or Reject if any fails.
// Guard order (fixed, tested by P-15):
//   1. Kill switch (S-4)      — cheapest, most decisive
//   2. Circuit breaker (S-3)   — may be Unknown (fail closed)
//   3. Concurrency (S-5)       — admit before consuming tokens
//   4. Rate limit (S-1)        — consume token only if admitted
//   5. Timeout (S-2)           — applied to returned context, not checked here
func (g *Gate) Check(ctx context.Context, capID string, principal string) (*Admission, *Reject)
```

### 2.2 Admission and Reject

```go
// Admission represents an admitted request.
// The caller MUST call Release() when execution completes (success, error, or timeout).
type Admission struct {
    capID    string
    deadline time.Time
    release  func()  // releases semaphore slot
    bucket   *tokenBucket  // for recording success/failure to breaker
}

// DeadlineContext returns a new context with the S-2 timeout deadline applied.
// R93-①: This is a context.WithTimeout wrapper — cancellation signal, NOT goroutine kill.
func (a *Admission) DeadlineContext(parent context.Context) (context.Context, context.CancelFunc)

// Release releases all held resources (semaphore slot).
// MUST be called on every path: success, error, timeout, panic-recover.
func (a *Admission) Release()

// RecordOutcome tells the breaker whether the execution succeeded or failed.
// Called after execution completes, before Release().
//
// R21-10 (R94 binding): RecordOutcome affects ONLY protection feedback state
// (breaker failure count, breaker state transition). It MUST NOT:
//   - alter Policy
//   - alter Plugin state
//   - alter Execution state
//   - append mutation audit records
//   - invoke Governance evaluation
//   - trigger retries
//   - trigger circuit recovery actions outside the breaker itself
//
// The allowed effect is: execution result → breaker feedback. Only.
// This keeps Protection as an admission boundary, not an execution controller.
func (a *Admission) RecordOutcome(err error)
```

```go
// Reject represents a blocked request.
type Reject struct {
    Action     string        // protection.* vocabulary (R93-④)
    RetryAfter time.Duration // hint for client; zero if not applicable
    HTTPStatus int           // 403, 429, 503
}
```

### 2.3 S-1 TokenBucket

```go
// TokenBucketSet manages per-(CapabilityID, principal_hash) buckets.
type TokenBucketSet struct {
    mu      sync.Mutex
    buckets map[string]*tokenBucket  // key = capID + ":" + principalHash
    clock   func() time.Time
}

// tokenBucket is a simple token bucket: capacity tokens, refill rate tokens/sec.
// Hand-rolled in ~30 LoC (R21-2: no golang.org/x/time/rate).
type tokenBucket struct {
    tokens   float64
    capacity float64
    refill   float64  // tokens per second
    last     time.Time
}

// Take returns true if a token was consumed, false if the bucket is empty.
// R93 accepted: rejected requests do NOT consume tokens (check before consume).
// Concurrency check (S-5) happens BEFORE Take, so a rejected-by-concurrency
// request never reaches Take.
func (t *tokenBucket) Take(now time.Time) bool

// Principal hashing: SHA-256 with a per-process random salt.
// The salt is generated at startup and never persisted — it exists only to
// prevent correlation across restarts. The hash is never logged in cleartext.
func principalHash(principal string, salt []byte) string
```

### 2.4 S-2 Timeout (R93-① binding)

```go
// TimeoutConfig holds the per-capability timeout.
type TimeoutConfig struct {
    Default  time.Duration  // 30s
    Override map[string]time.Duration  // per CapabilityID
}

// Apply wraps the parent context with a deadline.
// R93-①: This is a cancellation/deadline boundary, NOT a guaranteed termination primitive.
// The downstream plugin sees context.DeadlineExceeded via ctx.Done() — whether it
// actually stops depends on cooperative context handling.
// The Gate does NOT add any goroutine kill, os.Process.Kill, or similar mechanism.
func (tc *TimeoutConfig) Apply(parent context.Context, capID string) (context.Context, context.CancelFunc)
```

### 2.5 S-3 CircuitBreaker (R93-② binding)

```go
// BreakerState is a 4-state enum (R93-② adds Unknown).
type BreakerState int

const (
    BreakerClosed    BreakerState = iota  // normal operation
    BreakerOpen                             // rejecting all requests
    BreakerHalfOpen                         // admitting 1 probe request
    BreakerUnknown                          // R93-②: audit unreadable, fail closed
)

// BreakerSet manages per-CapabilityID breakers.
type BreakerSet struct {
    mu       sync.Mutex
    breakers map[string]*circuitBreaker
    audit    AuditReader   // reads from Phase 18/19 audit stream
    clock    func() time.Time
}

// circuitBreaker is hand-rolled in ~80 LoC (R21-2: no external dependency).
type circuitBreaker struct {
    state          BreakerState
    failureCount   int
    lastFailure    time.Time
    openedAt       time.Time
    config         BreakerConfig
}

type BreakerConfig struct {
    FailureThreshold int           // 5 consecutive failures
    Window           time.Duration // 60s sliding window
    Cooldown         time.Duration // 30s before half-open
}

// Evaluate checks the breaker state.
// R93-②: If audit is unreadable, returns BreakerUnknown → Reject.
// If audit returns truncated=true, failure count is a lower bound (P-9).
// R21-8 (R94): If evidence is truncated AND visible failures < threshold,
// state is Unknown (NOT Closed) — hidden portion could contain more failures.
// Only visible count ≥ threshold may prove Open.
func (b *circuitBreaker) Evaluate(now time.Time) (BreakerState, int, error)

// R21-8 decision table (binding):
//   truncated=false, failures < threshold  → Closed
//   truncated=true,  failures < threshold  → Unknown  (fail closed)
//   truncated=true,  failures ≥ threshold   → Open     (lower bound proves it)
//   audit error                           → Unknown   (fail closed)

// RecordOutcome records success or failure after execution.
// Called by Admission.RecordOutcome().
func (b *circuitBreaker) RecordOutcome(err error, now time.Time)
```

### 2.6 S-4 KillStore (R93-③ binding)

```go
// KillStore is the SINGLE persistence owner for protection kill-state.
// R93-③: Protection owns Protection State persistence.
// The in-memory map is only a runtime projection of persistent state.
type KillStore struct {
    mu       sync.RWMutex
    killed   map[string]bool  // capID -> killed (in-memory projection)
    store    KillPersistence  // persistent storage (storage v6)
    loaded   bool             // false until successful bootstrap load
    clock    func() time.Time
}

// KillPersistence is the storage interface (implemented by storage/sqlite or storage/memory).
type KillPersistence interface {
    LoadKills() (map[string]bool, error)           // bootstrap load
    SetKilled(capID string, killed bool) error     // write-through
    ListKills() ([]KillEntry, error)               // for :8082 read endpoint
}

// Bootstrap loads persistent kill-state at startup.
// R93-③: If load fails, KillStore.loaded stays false → IsKilled returns true
// for ALL capabilities (fail closed). The system does NOT default to "all un-killed"
// when it cannot verify its own protection state.
func (ks *KillStore) Bootstrap() error

// IsKilled checks the in-memory fast gate.
// If KillStore.loaded is false, returns true (fail closed, R93-③).
func (ks *KillStore) IsKilled(capID string) bool

// SetKilled updates persistent state first, then projects to in-memory.
// R93-③: Unidirectional flow: persistent → memory, never memory → persistent.
// The in-memory gate never independently mutates persistent state.
func (ks *KillStore) SetKilled(capID string, killed bool) error
```

### 2.7 S-5 Semaphore

```go
// SemaphoreSet manages per-CapabilityID semaphores.
type SemaphoreSet struct {
    mu      sync.Mutex
    sems    map[string]chan struct{}  // buffered channel as semaphore
    caps    map[string]int             // per-capability concurrency cap
    defaultCap int                     // 8
}

// Acquire tries to take a slot. Returns true if acquired, false if full.
// Does NOT block — R93 accepted: "reject does not wait indefinitely".
func (ss *SemaphoreSet) Acquire(capID string) bool

// Release returns a slot. MUST be called on every path.
// Called by Admission.Release().
func (ss *SemaphoreSet) Release(capID string)
```

---

## 3. Audit vocabulary (R21-6 / R93-④ binding)

Protection rejects produce **audit events**, not Policy mutation INTENT→CAS→OUTCOME.

```go
// AuditWriter writes protection.* events to the Phase 18 audit stream.
type AuditWriter interface {
    WriteEvent(ctx context.Context, event ProtectionEvent) error
}

// ProtectionEvent is a protection-specific audit event.
type ProtectionEvent struct {
    Timestamp time.Time
    Action    string  // protection.* vocabulary (see below)
    CapID     string
    Principal string  // hashed, never cleartext
    Detail    string  // human-readable context
}

// Protection action vocabulary (R93-④):
const (
    ActionRateLimited        = "protection.rate_limited"
    ActionTimeout            = "protection.timeout"
    ActionCircuitOpen        = "protection.circuit_open"
    ActionBreakerUnknown     = "protection.breaker_unknown"
    ActionKilled             = "protection.killed"
    ActionPrincipalKilled    = "protection.principal_killed"
    ActionConcurrencyExceeded = "protection.concurrency_exceeded"
)
```

**R93-④ invariant**: These events are recorded as **observations**, not as Policy
mutations. They do NOT flow through the management INTENT→CAS→OUTCOME pipeline
(Phase 17.2). They are written directly to the audit store with a `protection.*`
action namespace. The audit store's existing query surface (Phase 18/19) can filter
by `action LIKE 'protection.%'` to see protection pressure over time.

**R21-9 (R94 binding)**: Protection reject is **irreversible within the request**.
If recording a Protection rejection fails (audit store unavailable, write error),
the Protection decision itself **remains rejected** and MUST NOT be converted into
admission. The audit is *evidence* of the protection decision, not an authorization
prerequisite that can reverse it. The audit write failure should be observable through
the existing logging/operational path (Phase 19 `/metrics` or stderr), but the request
is still rejected.

```
Gate says Reject
Audit write fails
    ↓
still Reject (never: audit failure → allow request)
```

---

## 4. Storage schema (v6, additive)

### 4.1 `operation_protection` table

```sql
CREATE TABLE IF NOT EXISTS operation_protection (
    capability_id     TEXT PRIMARY KEY REFERENCES operation(name),
    rate_limit        INTEGER NOT NULL DEFAULT 60,     -- tokens per 60s
    timeout_seconds   INTEGER NOT NULL DEFAULT 30,
    breaker_threshold INTEGER NOT NULL DEFAULT 5,
    breaker_window_s  INTEGER NOT NULL DEFAULT 60,
    cooldown_seconds  INTEGER NOT NULL DEFAULT 30,
    concurrency_cap   INTEGER NOT NULL DEFAULT 8
);
```

### 4.2 `kill_state` table (S-4 persistence, R93-③)

```sql
CREATE TABLE IF NOT EXISTS kill_state (
    capability_id  TEXT PRIMARY KEY REFERENCES operation(name),
    killed         INTEGER NOT NULL DEFAULT 0,  -- 0 = alive, 1 = killed
    killed_at      TEXT,                        -- RFC 3339 timestamp
    killed_by      TEXT                         -- operator identity
);
```

### 4.3 `principal_kill_state` table (S-4 by-principal, optional)

```sql
CREATE TABLE IF NOT EXISTS principal_kill_state (
    principal_hash  TEXT PRIMARY KEY,  -- SHA-256 hash, never cleartext
    killed          INTEGER NOT NULL DEFAULT 0,
    killed_at       TEXT,
    killed_by       TEXT
);
```

All three tables are **additive** — existing v5 tables are unchanged. The migration
is forward-only: `CREATE TABLE IF NOT EXISTS`.

---

## 5. Wiring into the request boundary

The protection Gate sits **between** governance check and executor:

```
Request → external/v1 handler
         → governance.Policy check (frozen, unchanged)
         → protection.Gate.Check (NEW)
            → Admission (pass)  → executor.Execute(admission.DeadlineContext(ctx))
            → Reject (block)    → audit protection.* event → HTTP error response
```

**Wiring touch points** (3 files, all in non-frozen packages):

1. `internal/controlplane/server/server.go` — insert `gate.Check()` call between
   governance check and executor call. On Admission, wrap ctx with deadline. On
   Reject, write audit event and return HTTP error.
2. `internal/controlplane/server/server.go` — add `GET /management/v1/protection/kills`
   (R21-1) and extend `/metrics` with protection counters (R21-7).
3. `cmd/opscore/main.go` — instantiate `protection.Gate` with storage + audit deps,
   inject into server.

**Frozen packages touched**: ZERO. (R21-3)

---

## 6. Gate check order (P-15 binding)

The fixed order is:

```
1. KillStore.IsKilled(capID)           → Reject{Action: "protection.killed"}       → 403
2. KillStore.IsPrincipalKilled(hash)    → Reject{Action: "protection.principal_killed"} → 403
3. BreakerSet.Evaluate(capID)           → Reject{Action: "protection.circuit_open"} → 503
                                        → Reject{Action: "protection.breaker_unknown"} → 503
4. SemaphoreSet.Acquire(capID)          → Reject{Action: "protection.concurrency_exceeded"} → 503
5. TokenBucketSet.Take(capID, hash)     → Reject{Action: "protection.rate_limited"} → 429
6. TimeoutConfig.Apply(ctx, capID)      → applied to returned context (not a reject)
```

**Rationale**: Kill switch is cheapest and most decisive (one map lookup). Breaker
may need audit query (expensive). Concurrency before rate (R93 accepted: don't
consume tokens if concurrency is full). Rate before timeout (don't set up a deadline
if the request is rate-limited). Timeout is applied to the returned context, not
checked as a gate — it fires asynchronously during execution.

---

## 7. Property tests (P-1..P-20)

| # | Test | R93 binding | What it freezes |
|---|---|---|---|
| P-1 | `TestTokenBucketRefills` | — | Token bucket refills at configured rate |
| P-2 | `TestTokenBucketRejectsWhenEmpty` | — | Bucket with 0 tokens rejects |
| P-3 | `TestRejectedRequestsDontConsumeTokens` | — | Concurrency-rejected requests don't call Take |
| P-4 | `TestTimeoutAppliesDeadline` | — | DeadlineContext returns ctx with deadline |
| P-5 | `TestTimeoutDoesNotKillGoroutine` | R93-① | No goroutine kill / process kill / os.Exit in protection package |
| P-6 | `TestBreakerOpensAfterFailures` | — | N consecutive failures → Open |
| P-7 | `TestBreakerHalfOpensAfterCooldown` | — | After cooldown, admits 1 probe |
| P-8 | `TestBreakerUnknownOnUnreadableAudit` | R93-② | Audit error → Unknown → Reject |
| P-9 | `TestBreakerAcknowledgesTruncation` | R93-② | truncated=true → lower bound, not exact |
| P-10 | `TestKillStorePersistsAcrossRestart` | — | Kill set, restart, still killed |
| P-11 | `TestKillStoreStartupFailClosed` | R93-③ | Load error → all killed |
| P-12 | `TestKillStoreSingleOwner` | R93-③ | No second persistence path (AST: no other type implements KillPersistence) |
| P-13 | `TestSemaphoreBoundsConcurrency` | — | N+1th concurrent request rejected |
| P-14 | `TestSemaphoreReleasesOnAllPaths` | — | Release called on success, error, timeout |
| P-15 | `TestGateCheckOrder` | — | Kill → Breaker → Concurrency → Rate → Timeout |
| P-16 | `TestEveryRejectProducesAuditEvent` | R93-④ | Each Reject → protection.* event written |
| P-17 | `TestProtectionRejectNotPolicyMutation` | R93-④ | No INTENT/CAS/OUTCOME for protection rejects |
| P-18 | `TestFrozenPackagesZeroDiff` | R21-3 | git diff on frozen packages = empty |
| P-19 | `TestNoNewDependencies` | R21-2 | go.mod unchanged (stdlib only) |
| P-20 | `TestExternalV1Unchanged` | R21-0 | external/v1 types identical |
| P-21 | `TestBreakerTruncatedBelowThresholdIsUnknown` | R21-8 | truncated=true, failures=3, threshold=5 → Unknown |
| P-22 | `TestRejectSurvivesAuditFailure` | R21-9 | AuditWriter returns error → Reject still returned to caller |
| P-23 | `TestRecordOutcomeIsProtectionOnly` | R21-10 | AST scan: RecordOutcome does not reference Policy/Runtime/Plugin/Governance types |

---

## 8. Mutation tests (M-1..M-11)

| # | Mutation | Property that fails | R93 binding |
|---|---|---|---|
| M-1 | Remove token refill logic | P-1 | — |
| M-2 | Return true when bucket empty | P-2 | — |
| M-3 | Call Take before concurrency check | P-3 | — |
| M-4 | Add `runtime.Goexit()` or `os.Process.Kill` in timeout path | P-5 | R93-① |
| M-5 | Return BreakerClosed when audit errors | P-8 | R93-② |
| M-6 | Ignore `truncated` flag in audit response | P-9 | R93-② |
| M-7 | Set `loaded=true` even when load fails | P-11 | R93-③ |
| M-8 | Add a second KillPersistence implementation that writes directly | P-12 | R93-③ |
| M-9 | Remove `defer admission.Release()` in execute path | P-14 | — |
| M-10 | Reorder gate checks (e.g. rate before kill) | P-15 | — |
| M-11 | Use `management.Intent{Type: "protection"}` for reject audit | P-17 | R93-④ |
| M-12 | Return BreakerClosed when truncated=true and failures < threshold | P-21 | R21-8 |
| M-13 | Convert Reject to Admission when audit write fails | P-22 | R21-9 |
| M-14 | Add Policy/Governance mutation call inside RecordOutcome | P-23 | R21-10 |

---

## 9. `/metrics` extension (R21-7)

```
# TYPE protection_rejected_total counter
protection_rejected_total{guard="rate_limited"} 0
protection_rejected_total{guard="timeout"} 0
protection_rejected_total{guard="circuit_open"} 0
protection_rejected_total{guard="breaker_unknown"} 0
protection_rejected_total{guard="killed"} 0
protection_rejected_total{guard="principal_killed"} 0
protection_rejected_total{guard="concurrency_exceeded"} 0

# TYPE protection_admitted_total counter
protection_admitted_total 0
```

Counters are **exact** (not windowed), following Phase 18 discipline.

---

## 10. Default values (R93 accepted)

```
rate:        60 / 60s
concurrency: 8
timeout:     30s
breaker:     5 failures / 60s
cooldown:    30s
```

**R93 binding**: "Defaults are deployment policy, not Runtime Contract semantics.
Do not freeze these as immutable behavioral constants."

The defaults are stored in `operation_protection` table with `DEFAULT` constraints.
Operators can override per capability via the management write surface (Phase 17.2
`:8082`). The Runtime Contract does not reference these values — it only references
the *existence* of the protection gate.

---

## 11. File inventory (estimated)

| File | LoC (est.) | Description |
|---|---|---|
| `internal/protection/gate.go` | ~120 | Gate, Admission, Reject types |
| `internal/protection/tokenbucket.go` | ~60 | S-1 token bucket + principal hash |
| `internal/protection/timeout.go` | ~30 | S-2 timeout wrapper (R93-①) |
| `internal/protection/breaker.go` | ~100 | S-3 circuit breaker with Unknown state (R93-②) |
| `internal/protection/killstore.go` | ~80 | S-4 kill store, single owner (R93-③) |
| `internal/protection/semaphore.go` | ~40 | S-5 bounded concurrency |
| `internal/protection/audit.go` | ~50 | Protection.* event writer (R93-④) |
| `internal/protection/gate_test.go` | ~340 | P-1..P-23 property tests |
| `internal/protection/mutation_test.go` | ~230 | M-1..M-14 mutation tests |
| `internal/protection/ast_guard_test.go` | ~40 | Frozen import + no-exec guard |
| `internal/storage/sqlite/protection.go` | ~60 | KillPersistence impl |
| `internal/storage/memory/protection.go` | ~40 | In-memory KillPersistence for tests |
| `internal/controlplane/server/server.go` | +15 | Gate.Check wiring |
| `internal/controlplane/server/server.go` | +20 | /kills endpoint + /metrics extension |
| `cmd/opscore/main.go` | +10 | Gate instantiation |
| **Total** | **~1235** | **1 new package + 3 touches** |

---

## 12. Wiring order

1. Create `internal/protection/` package with type definitions (no logic)
2. Implement `TokenBucketSet` (S-1) + `principalHash`
3. Implement `TimeoutConfig.Apply` (S-2, R93-①)
4. Implement `BreakerSet` with `BreakerUnknown` state (S-3, R93-②)
5. Implement `KillStore` with `Bootstrap` fail-closed (S-4, R93-③)
6. Implement `SemaphoreSet` (S-5)
7. Implement `AuditWriter` with `protection.*` vocabulary (R93-④)
8. Implement `Gate.Check` composing all 5 guards
9. Add storage v6 schema migrations (3 new tables)
10. Implement `KillPersistence` in `storage/sqlite` and `storage/memory`
11. Wire `Gate` into `controlplane/server/server.go` (between governance and executor)
12. Add `GET /management/v1/protection/kills` + `/metrics` extension
13. Write P-1..P-23 property tests (23 tests, including R21-8/9/10 binding)
14. Write M-1..M-14 mutation tests (14 tests, including R21-8/9/10 mutations)
15. Write AST guard test
16. `gofmt -w` + `go build ./...` + `go vet ./...` + `go test ./...`
17. Commit (single commit, main not pushed)

---

## 13. Phase 20 dependency

Per R93 and ADR-046 §6:

> Phase 20 implementation (ADR-045) must land before Phase 21 consumes its trace data.

S-3 (CircuitBreaker) reads from the audit stream — this is Phase 18/19, not Phase 20.
Phase 20's trace data is *not* consumed by Phase 21. However, the sequencing rule
still applies: no two unresolved Major implementations silently interleave.

**Clean sequence**:
```
Phase 20 (ADR-045) implementation → sign-off → CLOSED
    ↓
Phase 21 (ADR-047/048) implementation
```

If Phase 21 needs trace-derived failure evidence in future (e.g. using trace spans
for breaker decisions), it must consume the *landed* Phase 20 contract, not an
anticipated implementation.

---

## 14. R94 verdict and modifications applied

**R94 = B — ACCEPT WITH MODIFICATIONS.**

ADR-047 architecture approved with three additional iron laws applied:

- **R21-8**: Truncated breaker evidence below threshold → Unknown (not Closed). Only
  visible count ≥ threshold may prove Open. Decision table:
  - `truncated=false, failures < threshold` → Closed
  - `truncated=true, failures < threshold` → Unknown (fail closed)
  - `truncated=true, failures ≥ threshold` → Open (lower bound proves it)
  - `audit error` → Unknown (fail closed)

- **R21-9**: Protection reject is irreversible within the request. Audit write failure
  does NOT convert rejection to admission. The audit is evidence of the decision, not
  an authorization prerequisite.

- **R21-10**: `Admission.RecordOutcome()` is protection feedback only. It may update
  breaker state only — MUST NOT mutate Policy, Runtime, Plugin, Execution state, or
  create management mutation audit records. Protection is an admission boundary, not
  an execution controller.

R93-① (timeout), R93-③ (kill store), R93-④ (audit vocabulary) confirmed clean PASS.
R93-② (breaker) PASS with R21-8 tightening. Gate ordering, metrics, storage v6, and
Phase 20 sequencing all ACCEPT.

Core design approved: Rate Limit + Cooperative Timeout + Circuit Breaker + Persistent
Kill Switch + Concurrency Cap, all as a peripheral admission layer, without reopening
Runtime Contract or turning Protection into a Control Plane.

**Next**: ADR-048 Implementation (file inventory, exact LoC per commit, mutation test
plan). No code before R95 sign-off.

Full text: this ADR (commit pending). Nothing implemented. ADR-021 three-tier discipline holds.
