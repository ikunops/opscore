# ADR-048 — Phase 21: Operational Protection (Implementation)

- **Status**: **ACCEPTED WITH MODIFICATIONS (R95 verdict B)**. Four additional iron laws
  applied per R95 sign-off: R21-11 (audit failure metrics counter), R21-12 (Evidence
  Reader abstraction for breaker), R21-13 (KillStore tri-state), R21-14 (semaphore
  lifecycle bound to Admission.Release only). All 14 iron laws (R21-0..R21-14) are
  binding. **Phase 21 three-tier sign-off COMPLETE.** Code implementation authorized
  after Phase 20 lands. ADR-046 (Scope) ACCEPTED at R93 (B, commit `b3b5017`).
  ADR-047 (Architecture) ACCEPTED at R94 (B, commit `15a0c25`).
- **Date**: 2026-08-13
- **Companion to**: ADR-046 (Scope), ADR-047 (Architecture), ADR-045 (Phase 20 Impl,
  must land first), ADR-021 (three-tier discipline, frozen)
- **Author**: OpsCore Plugin Runtime Workstream

---

## 0. Abstract

ADR-047 defined the package surface, type contracts, storage schema, and test plan.
This ADR defines the **implementation mechanics**: exact file contents, test verification
approach, mutation test methodology, and commit boundary.

**Binding constraints from R93 + R94 + R95** (14 iron laws, all must be mechanically enforced):

| Law | Constraint | Mechanical enforcement |
|---|---|---|
| R21-0 | external/v1 unchanged | P-20: `diff external/ external/` = empty |
| R21-1 | Kill state via :8082 GET | Route in `server.go` (`ProtectionReadMux`) |
| R21-2 | No new dependency | P-19: `diff go.mod go.mod` = empty |
| R21-3 | Frozen packages zero diff | P-18: `git diff internal/plugin/runtime/ internal/plugin/isolation/ internal/controlplane/hostregistry/ internal/platform/ internal/governance/` = empty |
| R21-4 | Reject = same shape as RBAC denial | Reject struct shape matches governance denial |
| R21-5 | Context propagation only | No goroutine-local; S-2 uses `context.WithTimeout` |
| R21-6 | Protection.* audit vocabulary | P-16, P-17, M-11 |
| R21-7 | Exact counters, not windowed | `/metrics` uses `atomic.AddInt64` |
| R21-8 | Truncated + below threshold → Unknown | P-21, M-12 |
| R21-9 | Reject survives audit failure | P-22, M-13 |
| R21-10 | RecordOutcome is feedback only | P-23, M-14 |
| R21-11 | Audit write failure → exact metrics counter | P-24, M-15 |
| R21-12 | Breaker depends on FailureEvidenceReader, not AuditStore | P-25, M-16 |
| R21-13 | KillStore state: Ready/Failed/Uninitialized (not just bool) | P-26, M-17 |
| R21-14 | Semaphore release only via Admission.Release() (no timeout auto-release) | P-27, M-18 |

---

## 1. File inventory (exact)

### 1.1 New package: `internal/protection/`

| File | LoC | Purpose |
|---|---|---|
| `gate.go` | ~130 | `Gate`, `Admission`, `Reject` types + `Check()` method |
| `tokenbucket.go` | ~65 | `TokenBucketSet`, `tokenBucket`, `principalHash()` |
| `timeout.go` | ~35 | `TimeoutConfig.Apply()` — R93-① / R21-8 |
| `breaker.go` | ~110 | `BreakerSet`, `circuitBreaker`, `BreakerState` (4-state), R21-8 decision table |
| `killstore.go` | ~90 | `KillStore`, `KillPersistence` interface, `Bootstrap()` fail-closed — R93-③ |
| `semaphore.go` | ~45 | `SemaphoreSet`, `Acquire()`, `Release()` |
| `audit.go` | ~55 | `AuditWriter`, `ProtectionEvent`, 7 action constants — R93-④ / R21-9 |
| `gate_test.go` | ~420 | P-1..P-27 (27 property tests) |
| `mutation_test.go` | ~290 | M-1..M-18 (18 mutation tests) |
| `ast_guard_test.go` | ~45 | Frozen import guard + `TestNoExecMethod` |
| **Subtotal** | **~1185** | |

### 1.2 Storage implementations

| File | LoC | Purpose |
|---|---|---|
| `internal/storage/sqlite/protection.go` | ~65 | `KillPersistence` SQLite impl + 3 table migrations |
| `internal/storage/memory/protection.go` | ~45 | In-memory `KillPersistence` for tests |
| **Subtotal** | **~110** | |

### 1.3 Touch points (non-frozen)

| File | ΔLoC | Change |
|---|---|---|
| `internal/controlplane/server/server.go` | +18 | Insert `gate.Check()` between governance and executor |
| `internal/controlplane/server/server.go` | +25 | `GET /management/v1/protection/kills` + `/metrics` counters |
| `cmd/opscore/main.go` | +12 | Instantiate `protection.Gate`, inject into server |
| **Subtotal** | **+55** | |

### 1.4 Total

**~1390 LoC**: 1 new package (~1225) + 2 storage impls (~110) + 3 touch points (+55).

---

## 2. Property test details (P-1..P-27)

### R93-① binding (S-2 Timeout)

**P-5 `TestTimeoutDoesNotKillGoroutine`**:
- **Method**: AST scan of all `.go` files in `internal/protection/` for forbidden
  function calls: `runtime.Goexit`, `os.Exit`, `os.Process.Kill`, `signal.Kill`,
  `syscall.Kill`, `runtime.GoSched` (as termination proxy).
- **Pass condition**: zero matches.
- **Mutation M-4**: Add `runtime.Goexit()` in `TimeoutConfig.Apply` → P-5 fails.

### R93-② binding (S-3 Breaker)

**P-8 `TestBreakerUnknownOnUnreadableAudit`**:
- **Method**: Inject `AuditReader` that returns `error` on `ReadFailures()`. Call
  `BreakerSet.Evaluate("cap-1")`. Assert returned state is `BreakerUnknown`.
- **Pass condition**: `state == BreakerUnknown`.
- **Mutation M-5**: Return `BreakerClosed` when audit errors → P-8 fails.

**P-9 `TestBreakerAcknowledgesTruncation`**:
- **Method**: Inject `AuditReader` returning `{failures: 3, truncated: true}`. Call
  `Evaluate("cap-1")` with threshold=5. Assert state is `BreakerUnknown` (not Closed).
  Then inject `{failures: 5, truncated: true}`. Assert state is `BreakerOpen` (lower
  bound proves threshold exceeded).
- **Pass condition**: truncated+below → Unknown; truncated+at-or-above → Open.
- **Mutation M-6**: Ignore `truncated` flag → P-9 fails (returns Closed for 3 failures).

### R21-8 binding (R94 — Truncated evidence rule)

**P-21 `TestBreakerTruncatedBelowThresholdIsUnknown`**:
- **Method**: Full decision table test:
  | truncated | failures | threshold | expected state |
  |---|---|---|---|
  | false | 3 | 5 | Closed |
  | true | 3 | 5 | Unknown |
  | true | 5 | 5 | Open |
  | true | 7 | 5 | Open |
  | error | — | 5 | Unknown |
- **Pass condition**: all 5 rows match.
- **Mutation M-12**: Return `BreakerClosed` when `truncated=true && failures < threshold`
  → P-21 fails on row 2.

### R93-③ binding (S-4 KillStore)

**P-11 `TestKillStoreStartupFailClosed`**:
- **Method**: Inject `KillPersistence` whose `LoadKills()` returns `error`. Call
  `KillStore.Bootstrap()`. Assert `Bootstrap()` returns error. Assert `IsKilled("any-cap")`
  returns `true` (fail closed — all capabilities treated as killed).
- **Pass condition**: `loaded == false` → `IsKilled(any) == true`.
- **Mutation M-7**: Set `loaded = true` even when `LoadKills()` fails → P-11 fails
  (`IsKilled("any-cap")` returns false).

**P-12 `TestKillStoreSingleOwner`**:
- **Method**: AST scan — search all `.go` files in `internal/` for types implementing
  `KillPersistence` interface (methods `LoadKills`, `SetKilled`, `ListKills`). Assert
  exactly 2 implementations exist: `storage/sqlite.protectionStore` and
  `storage/memory.protectionStore` (test double). No third implementation in
  `controlplane/`, `plugin/`, or `protection/` itself.
- **Pass condition**: exactly 2 implementations, both in `storage/`.
- **Mutation M-8**: Add a `KillPersistence` implementation in `controlplane/server/`
  → P-12 fails (3 implementations found).

### R93-④ binding (R21-6 Audit vocabulary)

**P-16 `TestEveryRejectProducesAuditEvent`**:
- **Method**: For each of the 7 reject paths (killed, principal_killed, circuit_open,
  breaker_unknown, concurrency_exceeded, rate_limited, timeout), trigger the path and
  assert `AuditWriter.WriteEvent()` was called with the correct `protection.*` action.
- **Pass condition**: 7/7 events written with correct action strings.
- **Mutation M-10**: Skip audit write for one path → P-16 fails (6/7).

**P-17 `TestProtectionRejectNotPolicyMutation`**:
- **Method**: AST scan of `internal/protection/` for imports/references to
  `management.Intent`, `management.CAS`, `management.Outcome`, `governancepolicy.*`.
  Assert zero matches.
- **Pass condition**: no Policy mutation types referenced.
- **Mutation M-11**: Import `management.Intent` and use it for audit → P-17 fails.

### R21-9 binding (R94 — Reject is irreversible)

**P-22 `TestRejectSurvivesAuditFailure`**:
- **Method**: Inject `AuditWriter` whose `WriteEvent()` returns `error`. Trigger a
  rate-limit reject path. Assert `Gate.Check()` still returns `*Reject` (not `*Admission`).
  Assert the returned Reject has the correct action (`protection.rate_limited`).
- **Pass condition**: `Reject != nil && Admission == nil` even when audit write fails.
- **Mutation M-13**: Convert Reject to Admission when audit write fails → P-22 fails
  (`Admission != nil`).

### R21-10 binding (R94 — RecordOutcome scope)

**P-23 `TestRecordOutcomeIsProtectionOnly`**:
- **Method**: AST scan of `internal/protection/` for references to `Policy`, `Runtime`,
  `Plugin`, `Governance`, `management.*` types within the `RecordOutcome` method body.
  Assert zero matches. Additionally, call `RecordOutcome(errors.New("test"))` and verify
  no side effects beyond breaker state change (no storage writes, no governance calls,
  no audit mutations).
- **Pass condition**: RecordOutcome body references only `circuitBreaker` types.
- **Mutation M-14**: Add `governancepolicy.Evaluate()` call inside RecordOutcome → P-23
  fails (AST scan finds governance reference).

### R21-11 binding (R95 — Audit failure metrics counter)

**P-24 `TestAuditFailureIncrementsCounter`**:
- **Method**: Inject `AuditWriter` whose `WriteEvent()` returns `error`. Trigger a
  reject path. Assert `protection_audit_write_failed_total` counter incremented by 1.
  Assert Reject still returned (R21-9 holds). Assert no recursive audit write attempted.
- **Pass condition**: counter = 1, Reject != nil, no recursion.
- **Mutation M-15**: Remove counter increment → P-24 fails (counter = 0).

### R21-12 binding (R95 — Evidence Reader abstraction)

**P-25 `TestBreakerDependsOnEvidenceReader`**:
- **Method**: AST scan of `internal/protection/` for direct imports of
  `storage/audit` or `AuditStore`. Assert zero matches. Verify breaker depends on
  `FailureEvidenceReader` interface only.
  ```go
  type FailureEvidenceReader interface {
      RecentFailures(capabilityID string) (FailureWindow, error)
  }
  type FailureWindow struct {
      Count     int
      Truncated bool
  }
  ```
- **Pass condition**: no `storage/audit` imports; breaker uses `FailureEvidenceReader`.
- **Mutation M-16**: Import `storage/audit` directly in breaker → P-25 fails.

### R21-13 binding (R95 — KillStore tri-state)

**P-26 `TestKillStoreTriState`**:
- **Method**: Verify `KillStoreState` enum has 3 states: `Uninitialized`, `Ready`,
  `Failed`. Test: (1) before Bootstrap → `Uninitialized` → `IsKilled = true` (fail
  closed). (2) Bootstrap success with empty data → `Ready` → `IsKilled = false` (no
  kills). (3) Bootstrap error → `Failed` → `IsKilled = true` (fail closed). (4)
  `GET /management/v1/protection/kills` returns state field: `ready` / `failed` /
  `uninitialized` — not just `kills: []`.
- **Pass condition**: 3 states distinguished; empty ≠ failed.
- **Mutation M-17**: Use `loaded bool` instead of tri-state → P-26 fails (cannot
  distinguish empty from failed in API response).

### R21-14 binding (R95 — Semaphore lifecycle)

**P-27 `TestTimeoutDoesNotReleaseSemaphoreEarly`**:
- **Method**: Acquire semaphore (admission). Start long-running execution. Trigger
  timeout (context deadline exceeded). Assert semaphore slot is NOT released until
  `Admission.Release()` is explicitly called. Assert no goroutine watcher, no
  context.Done callback, no automatic release on timeout.
- **Pass condition**: slot held after timeout; released only after `Release()`.
- **Mutation M-18**: Add `context.Done()` callback that releases semaphore → P-27
  fails (slot released before `Release()` called).

---

## 3. Remaining property tests (P-1..P-4, P-6..P-7, P-10, P-13..P-15, P-18..P-20, P-24..P-27)

| # | Test | Method | Pass condition |
|---|---|---|---|
| P-1 | `TestTokenBucketRefills` | Empty bucket, wait 1s, Take → true | tokens refilled |
| P-2 | `TestTokenBucketRejectsWhenEmpty` | Full drain, Take → false | empty bucket rejects |
| P-3 | `TestRejectedRequestsDontConsumeTokens` | Concurrency-rejected → Take never called | bucket unchanged |
| P-4 | `TestTimeoutAppliesDeadline` | `DeadlineContext(ctx)` → `ctx.Deadline()` non-zero | deadline set |
| P-6 | `TestBreakerOpensAfterFailures` | 5 consecutive `RecordOutcome(err)` → Open | threshold reached |
| P-7 | `TestBreakerHalfOpensAfterCooldown` | Open + wait 30s → HalfOpen, admit 1 | cooldown works |
| P-10 | `TestKillStorePersistsAcrossRestart` | SetKilled, new KillStore, Bootstrap → still killed | persistence works |
| P-13 | `TestSemaphoreBoundsConcurrency` | Acquire N times, N+1th → false | bounded |
| P-14 | `TestSemaphoreReleasesOnAllPaths` | Acquire, Release on success/error/timeout → slot freed | all paths release |
| P-15 | `TestGateCheckOrder` | Mock each guard, assert call order | kill→breaker→concurrency→rate→timeout |
| P-18 | `TestFrozenPackagesZeroDiff` | `git diff --name-only` on frozen dirs = empty | zero diff |
| P-19 | `TestNoNewDependencies` | `git diff go.mod go.sum` = empty | stdlib only |
| P-20 | `TestExternalV1Unchanged` | `git diff external/` = empty | unchanged |

---

## 4. Mutation test methodology

Each mutation test follows the same pattern:

1. **Apply mutation**: Temporarily modify the source code (e.g., comment out a line,
   change a comparison operator, add a forbidden call).
2. **Run corresponding property test**: The test must FAIL.
3. **Revert mutation**: Restore original code.
4. **Run test again**: The test must PASS.

This is **manual mutation testing** (no framework like `go-mutesting`). The mutation
tests in `mutation_test.go` are **documentation tests** — they describe the mutation
and assert which property test should fail. The actual mutation is applied by the
developer during code review, not at runtime.

**Exception**: M-4, M-5, M-6, M-7, M-8, M-12, M-13, M-14 are **AST-backed** — they
can be verified mechanically by scanning for the mutated pattern. These run as real
tests, not just documentation.

---

## 5. Implementation details

### 5.1 TokenBucket (S-1)

```go
func (t *tokenBucket) Take(now time.Time) bool {
    elapsed := now.Sub(t.last).Seconds()
    t.tokens = min(t.capacity, t.tokens + elapsed * t.refill)
    t.last = now
    if t.tokens >= 1 {
        t.tokens -= 1
        return true
    }
    return false
}
```

Key: `Take` is called **after** concurrency check (S-5) passes. Rejected requests
never reach `Take`. R93 accepted: "rejected requests do not consume tokens."

### 5.2 CircuitBreaker Evaluate (S-3, R21-8)

```go
func (b *circuitBreaker) Evaluate(now time.Time) (BreakerState, int, error) {
    if b.state == BreakerOpen {
        if now.Sub(b.openedAt) >= b.config.Cooldown {
            return BreakerHalfOpen, b.failureCount, nil
        }
        return BreakerOpen, b.failureCount, nil
    }
    if b.state == BreakerHalfOpen {
        return BreakerHalfOpen, b.failureCount, nil
    }
    // Closed state — check audit for failures
    result, err := b.audit.ReadFailures(b.capID, b.config.Window)
    if err != nil {
        return BreakerUnknown, 0, err  // R93-2: fail closed
    }
    if result.Truncated && result.Count < b.config.FailureThreshold {
        return BreakerUnknown, result.Count, nil  // R21-8: truncated + below threshold
    }
    if result.Count >= b.config.FailureThreshold {
        b.state = BreakerOpen
        b.openedAt = now
        return BreakerOpen, result.Count, nil
    }
    return BreakerClosed, result.Count, nil
}
```

### 5.3 KillStore Bootstrap (S-4, R93-③)

```go
func (ks *KillStore) Bootstrap() error {
    kills, err := ks.store.LoadKills()
    if err != nil {
        ks.loaded = false  // R93-3: stays false → IsKilled returns true for all
        return fmt.Errorf("kill store bootstrap failed: %w", err)
    }
    ks.mu.Lock()
    ks.killed = kills
    ks.loaded = true
    ks.mu.Unlock()
    return nil
}

func (ks *KillStore) IsKilled(capID string) bool {
    ks.mu.RLock()
    defer ks.mu.RUnlock()
    if !ks.loaded {
        return true  // R93-3: fail closed — cannot verify, assume killed
    }
    return ks.killed[capID]
}
```

### 5.4 Gate.Check with R21-9

```go
func (g *Gate) Check(ctx context.Context, capID string, principal string) (*Admission, *Reject) {
    // 1. Kill switch (S-4)
    if g.kills.IsKilled(capID) {
        g.auditWrite(ctx, ProtectionEvent{Action: ActionKilled, CapID: capID, ...})  // R21-9: ignore error
        return nil, &Reject{Action: ActionKilled, HTTPStatus: 403}
    }
    // ... (principal kill, breaker, concurrency, rate) ...
    return &Admission{...}, nil
}

// R21-9: audit write failure does NOT reverse the reject
func (g *Gate) auditWrite(ctx context.Context, event ProtectionEvent) {
    if err := g.audit.WriteEvent(ctx, event); err != nil {
        // Log to stderr/metrics — but do NOT convert reject to admission
        log.Printf("protection audit write failed: %v (reject stands)", err)
    }
}
```

---

## 6. Commit strategy

**Single commit** (same as Phase 20 ADR-045 pattern):

```
Phase 21: Operational Protection implementation (ADR-048)

internal/protection: new peripheral package (~1185 LoC)
  - S-1 TokenBucket (rate limit, per capID + principal_hash)
  - S-2 TimeoutConfig (cooperative context cancellation, R93-1)
  - S-3 BreakerSet (4-state: Closed/Open/HalfOpen/Unknown, R21-8)
  - S-4 KillStore (single persistence owner, fail-closed startup, R93-3)
  - S-5 SemaphoreSet (bounded concurrency)
  - Gate (5-guard admission: kill→breaker→concurrency→rate→timeout)
  - AuditWriter (protection.* vocabulary, R21-9 irreversible reject)
  - 27 property tests + 18 mutation tests + AST guard

storage/sqlite: KillPersistence impl + 3 additive tables (v6)
storage/memory: KillPersistence test double
controlplane/server: gate wiring (+18 LoC) + /kills + /metrics (+25 LoC)
cmd/opscore: Gate instantiation (+12 LoC)

Iron laws: R21-0..R21-14 all mechanically enforced.
Frozen packages: zero diff.
No new dependencies.
```

**Prerequisite**: Phase 20 implementation (ADR-045) must be committed first.
R94 confirmed: "Phase 20 implementation must land before Phase 21 implementation."

---

## 7. Wiring order (detailed)

1. Create `internal/protection/` directory
2. Write type definitions: `gate.go` (Gate, Admission, Reject)
3. Write `tokenbucket.go` (TokenBucketSet, tokenBucket, principalHash)
4. Write `timeout.go` (TimeoutConfig.Apply — R93-①)
5. Write `breaker.go` (BreakerSet, circuitBreaker, 4-state, R21-8 decision table)
6. Write `killstore.go` (KillStore, KillPersistence, Bootstrap fail-closed — R93-③)
7. Write `semaphore.go` (SemaphoreSet, Acquire, Release)
8. Write `audit.go` (AuditWriter, ProtectionEvent, 7 actions, R21-9 — R93-④)
9. Write `gate.go` Check method (compose 5 guards, R21-9 auditWrite)
10. Add storage v6 migrations (3 tables) in `storage/sqlite/migrations.go`
11. Write `storage/sqlite/protection.go` (KillPersistence impl)
12. Write `storage/memory/protection.go` (test double)
13. Write `gate_test.go` (P-1..P-27)
14. Write `mutation_test.go` (M-1..M-18)
15. Write `ast_guard_test.go` (frozen import + no-exec + single-owner)
16. Wire gate into `controlplane/server/server.go` (+18 LoC)
17. Add protection read routes in `controlplane/server/server.go` via `Server.ProtectionReadMux()` (+25 LoC)
18. Instantiate gate in `cmd/opscore/main.go` (+12 LoC)
19. `gofmt -w internal/protection/ internal/storage/sqlite/protection.go internal/storage/memory/protection.go internal/controlplane/server/server.go cmd/opscore/main.go`
20. `export GOTOOLCHAIN=local GOSUMDB=off && go build ./... && go vet ./... && go test ./...`
21. Commit (single commit, main not pushed)

---

## 8. Phase 20 prerequisite

Per R93 and R94:

> Phase 20 implementation (ADR-045) must land before Phase 21 implementation.

**Status**: ADR-045 is ACCEPTED (R92 verdict A) but implementation code is NOT yet
committed. The Phase 20 wiring order (ADR-045 §8) has 12 steps that must be executed
and committed before Phase 21's first commit.

**Plan**: Land Phase 20 implementation first (separate commit), verify
`go build ./... && go vet ./... && go test ./...` passes, then proceed with Phase 21
implementation per this ADR.

Phase 21 does NOT consume Phase 20 trace data (S-3 reads from Phase 18/19 audit stream).
The sequencing is about avoiding two unresolved Major implementations in flight
simultaneously, not about a technical dependency.

---

## 9. R95 verdict and modifications applied

**R95 = B — ACCEPT WITH MODIFICATION.**

Phase 21 Implementation approved with four additional iron laws:

- **R21-11**: Protection audit write failure must increment `protection_audit_write_failed_total`
  exact counter (atomic, exposed to `/metrics`, no Reject behavior change, no recursive audit).
  Prevents false-clean when protection is rejecting but audit system is unavailable.

- **R21-12**: Breaker depends on `FailureEvidenceReader` interface, NOT directly on
  `AuditStore`. Maintains Phase 18/19 Evidence Producer/Consumer separation. Protection
  is an Evidence Consumer — no coupling to audit implementation details.
  ```go
  type FailureEvidenceReader interface {
      RecentFailures(capabilityID string) (FailureWindow, error)
  }
  ```

- **R21-13**: KillStore state must be tri-state: `Uninitialized` / `Ready` / `Failed`
  (not just `loaded bool`). Management endpoint must distinguish "no kill rules" from
  "kill state unavailable" — prevents false-clean in UI/automation.

- **R21-14**: Semaphore lifecycle bound to `Admission.Release()` only. No timeout
  auto-release, no goroutine watcher, no context.Done callback. Context cancellation ≠
  resource completion (R21-5). Test: `TestTimeoutDoesNotReleaseSemaphoreEarly`.

All S-1..S-5 components confirmed ✅. All R93-①..④ and R21-8..10 confirmed PASS.
No scope expansion, no OTel, no UI, no distributed control plane, no frozen package
modification, no governance pipeline change.

**Phase 21 three-tier sign-off COMPLETE**:
- R93 (Scope, ADR-046) = B — commit `b3b5017`
- R94 (Architecture, ADR-047) = B — commit `15a0c25`
- R95 (Implementation, ADR-048) = B — this commit

**Next steps** (fixed order per R95):
1. Phase 20 implementation landing (ADR-045 code)
2. Phase 20 gate verification (build/vet/test)
3. Phase 21 implementation commit (this ADR's code)
4. Phase 21 mutation verification
5. Phase 22 candidates evaluation

Continue maintaining ADR-021 three-tier discipline.
