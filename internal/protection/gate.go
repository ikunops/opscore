package protection

import (
	"context"
	"sync/atomic"
	"time"
)

// Gate is the sole protection entry point. It composes the five guards in a
// fixed order (P-15): kill → principal-kill → breaker → concurrency → rate →
// timeout (timeout is applied to the returned context, not a gate).
type Gate struct {
	kills    *KillStore
	breaker  *BreakerSet
	sem      *SemaphoreSet
	buckets  *TokenBucketSet
	audit    AuditWriter
	timeout  *TimeoutConfig
	salt     []byte
	clock    func() time.Time
	counters *counters
}

type counters struct {
	admitted            atomic.Int64
	killed              atomic.Int64
	principalKilled     atomic.Int64
	circuitOpen         atomic.Int64
	breakerUnknown      atomic.Int64
	concurrencyExceeded atomic.Int64
	rateLimited         atomic.Int64
	auditWriteFailed    atomic.Int64
}

// Config configures a Gate.
type Config struct {
	KillStore *KillStore
	Breaker   *BreakerSet
	Sem       *SemaphoreSet
	Buckets   *TokenBucketSet
	Audit     AuditWriter
	Timeout   *TimeoutConfig
	Salt      []byte
	Clock     func() time.Time
}

// New builds a Gate from its components.
func New(cfg Config) *Gate {
	if cfg.Timeout == nil {
		cfg.Timeout = NewTimeoutConfig()
	}
	if cfg.Salt == nil {
		cfg.Salt = newSalt()
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	return &Gate{
		kills:    cfg.KillStore,
		breaker:  cfg.Breaker,
		sem:      cfg.Sem,
		buckets:  cfg.Buckets,
		audit:    cfg.Audit,
		timeout:  cfg.Timeout,
		salt:     cfg.Salt,
		clock:    cfg.Clock,
		counters: &counters{},
	}
}

// Check evaluates all guards. Returns an *Admission if the request is admitted,
// or a *Reject if it is blocked. Exactly one is non-nil.
//
// Guard order (fixed, P-15):
//  1. kill switch       → 403
//  2. principal kill      → 403
//  3. breaker (open/unknown) → 503
//  4. concurrency cap     → 503
//  5. rate limit          → 429
//  6. timeout             → applied to the returned context (not a reject)
func (g *Gate) Check(ctx context.Context, capID string, principal string) (*Admission, *Reject) {
	now := g.clock()
	hash := principalHash(principal, g.salt)

	// 1. Kill switch (cheapest, most decisive).
	if g.kills != nil && g.kills.IsKilled(capID) {
		g.counters.killed.Add(1)
		g.auditWrite(ctx, ProtectionEvent{Action: ActionKilled, CapID: capID, Principal: hash, Timestamp: now})
		return nil, &Reject{Action: ActionKilled, HTTPStatus: 403}
	}

	// 2. Principal kill.
	if g.kills != nil && g.kills.IsPrincipalKilled(hash) {
		g.counters.principalKilled.Add(1)
		g.auditWrite(ctx, ProtectionEvent{Action: ActionPrincipalKilled, CapID: capID, Principal: hash, Timestamp: now})
		return nil, &Reject{Action: ActionPrincipalKilled, HTTPStatus: 403}
	}

	// 3. Circuit breaker (may be Unknown → fail closed).
	if g.breaker != nil {
		state, _, err := g.breaker.Evaluate(capID, now)
		if state == BreakerOpen || state == BreakerUnknown || err != nil {
			action := ActionCircuitOpen
			c := &g.counters.circuitOpen
			if state == BreakerUnknown || err != nil {
				action = ActionBreakerUnknown
				c = &g.counters.breakerUnknown
			}
			c.Add(1)
			g.auditWrite(ctx, ProtectionEvent{Action: action, CapID: capID, Principal: hash, Timestamp: now})
			return nil, &Reject{Action: action, HTTPStatus: 503}
		}
	}

	// 4. Concurrency cap (admit before consuming tokens).
	if g.sem != nil && !g.sem.Acquire(capID) {
		g.counters.concurrencyExceeded.Add(1)
		g.auditWrite(ctx, ProtectionEvent{Action: ActionConcurrencyExceeded, CapID: capID, Principal: hash, Timestamp: now})
		return nil, &Reject{Action: ActionConcurrencyExceeded, HTTPStatus: 503}
	}

	// 5. Rate limit (consume a token only if concurrency admitted).
	if g.buckets != nil && !g.buckets.Take(capID, hash) {
		if g.sem != nil {
			g.sem.Release(capID) // roll back the slot we just took
		}
		g.counters.rateLimited.Add(1)
		g.auditWrite(ctx, ProtectionEvent{Action: ActionRateLimited, CapID: capID, Principal: hash, Timestamp: now})
		return nil, &Reject{Action: ActionRateLimited, HTTPStatus: 429}
	}

	// 6. Timeout is applied to the returned context, not checked as a gate.
	g.counters.admitted.Add(1)
	a := &Admission{
		capID: capID,
		gate:  g,
		release: func() {
			if g.sem != nil {
				g.sem.Release(capID)
			}
		},
		clock: g.clock,
	}
	return a, nil
}

// Admission represents an admitted request. The caller MUST call Release() when
// execution completes (success, error, or timeout) and SHOULD call
// RecordOutcome(err) with the execution result before Release().
type Admission struct {
	capID   string
	gate    *Gate
	release func()
	clock   func() time.Time
}

// DeadlineContext returns a context with the S-2 cooperative timeout applied.
// R93-①: cancellation signal only, not a termination primitive. The returned
// CancelFunc is folded into Release() — there is NO automatic release on
// timeout (R21-14: semaphore release is bound to Release() only).
func (a *Admission) DeadlineContext(parent context.Context) (context.Context, context.CancelFunc) {
	if a.gate == nil || a.gate.timeout == nil {
		return parent, func() {}
	}
	return a.gate.timeout.WithDeadline(parent, a.capID)
}

// RecordOutcome tells the breaker whether the execution succeeded or failed
// (R21-10: protection feedback only — no Policy/Runtime/Plugin mutation).
func (a *Admission) RecordOutcome(failed bool) {
	if a.gate == nil || a.gate.breaker == nil {
		return
	}
	a.gate.breaker.RecordOutcome(a.capID, failed, a.clock())
}

// Release releases all held resources (semaphore slot + timeout cancel). MUST
// be called on every path. Idempotent.
func (a *Admission) Release() {
	if a.release != nil {
		a.release()
		a.release = nil
	}
}

// KillState exposes the kill store tri-state for the read surface (R21-13).
func (g *Gate) KillState() KillStoreState {
	if g.kills == nil {
		return KillStateUninitialized
	}
	return g.kills.State()
}

// ListKills returns persisted kill entries for the read surface.
func (g *Gate) ListKills() ([]KillEntry, error) {
	if g.kills == nil {
		return nil, nil
	}
	return g.kills.List()
}

// KillStore returns the kill store the Gate reads (Phase 22.2 P22-2: operator
// mutations MUST write through this same single owner, never a second KillState).
func (g *Gate) KillStore() *KillStore { return g.kills }

// Audit returns the audit writer the Gate uses, so operator-initiated
// kill/release observations are recorded on the same audit stream.
func (g *Gate) Audit() AuditWriter { return g.audit }

// SnapshotMetrics returns exact counters (R21-7).
func (g *Gate) SnapshotMetrics() Metrics {
	return Metrics{
		Admitted:            g.counters.admitted.Load(),
		Killed:              g.counters.killed.Load(),
		PrincipalKilled:     g.counters.principalKilled.Load(),
		CircuitOpen:         g.counters.circuitOpen.Load(),
		BreakerUnknown:      g.counters.breakerUnknown.Load(),
		ConcurrencyExceeded: g.counters.concurrencyExceeded.Load(),
		RateLimited:         g.counters.rateLimited.Load(),
		AuditWriteFailed:    g.counters.auditWriteFailed.Load(),
	}
}

// auditWrite records a protection.* observation. R21-9: a write failure MUST
// NOT reverse an already-decided reject — the audit is evidence of the decision,
// not an authorization prerequisite. R21-11: a write failure increments the
// audit_write_failed counter (no recursion, no behavior change).
func (g *Gate) auditWrite(ctx context.Context, ev ProtectionEvent) {
	if g.audit == nil {
		return
	}
	if err := g.audit.WriteEvent(ctx, ev); err != nil {
		g.counters.auditWriteFailed.Add(1)
	}
}
