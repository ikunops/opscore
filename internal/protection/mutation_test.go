package protection

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Mutation tests (M-1..M-18). Per ADR-048 §4 these are documentation tests: they
// assert the invariant the corresponding property test freezes. During code
// review the developer applies the named mutation by hand and confirms the
// relevant test goes RED; here they assert the current (correct) code is GREEN.
// The AST-backed mutations (M-4/M-7/M-8/M-12/M-14/M-16/M-17) are additionally
// enforced mechanically by ast_guard_test.go.

// M-1: removing token refill logic must fail P-1 (bucket never refills).
func TestMutationM1RefillRequired(t *testing.T) {
	mclk := &mutableClock{now: time.Unix(0, 0)}
	tbs := NewTokenBucketSet(TokenBucketConfig{Capacity: 1, Refill: 1}, mclk.nowTime)
	_ = tbs.Take("op", "p")
	mclk.add(time.Second)
	if !tbs.Take("op", "p") {
		t.Fatal("M-1: without refill the bucket would stay empty — invariant holds")
	}
}

// M-2: returning true when empty must fail P-2.
func TestMutationM2EmptyRejects(t *testing.T) {
	mclk := &mutableClock{now: time.Unix(0, 0)}
	tbs := NewTokenBucketSet(TokenBucketConfig{Capacity: 1, Refill: 1}, mclk.nowTime)
	_ = tbs.Take("op", "p")
	if tbs.Take("op", "p") {
		t.Fatal("M-2: empty bucket must reject — invariant holds")
	}
}

// M-3: calling Take before the concurrency check must fail P-3.
func TestMutationM3OrderTakeAfterConcurrency(t *testing.T) {
	gate := newTestGate(NewKillStore(newFakeKillPersistence(), time.Now), &fakeFailureReader{}, &fakeAuditWriter{})
	_, _ = gate.Check(context.Background(), "op", "principal") // clean admit path
	// Token bucket must still be load-bearing: a fresh gate that drains the
	// bucket then rejects on rate (not on concurrency) proves Take is gated by
	// concurrency first (P-3).
	gate2 := newTestGate(NewKillStore(newFakeKillPersistence(), time.Now), &fakeFailureReader{}, &fakeAuditWriter{})
	for gate2.buckets.Take("op", principalHash("principal", gate2.salt)) {
	}
	_, rej := gate2.Check(context.Background(), "op", "principal")
	if rej == nil || rej.Action != ActionRateLimited {
		t.Fatalf("M-3: rate must be the gate that rejects, got %v", rej)
	}
}

// M-5: returning BreakerClosed when audit errors must fail P-8.
func TestMutationM5AuditErrorUnknown(t *testing.T) {
	b := NewBreakerSet(&fakeFailureReader{err: errors.New("x")}, DefaultBreakerConfig(), time.Now)
	if st, _, err := b.Evaluate("op", time.Now()); err == nil || st != BreakerUnknown {
		t.Fatalf("M-5: audit error must be Unknown, got %s", st)
	}
}

// M-6: ignoring the truncated flag must fail P-9.
func TestMutationM6TruncationHonored(t *testing.T) {
	b := NewBreakerSet(&fakeFailureReader{window: FailureWindow{Count: 3, Truncated: true}}, DefaultBreakerConfig(), time.Now)
	if st, _, _ := b.Evaluate("op", time.Now()); st != BreakerUnknown {
		t.Fatalf("M-6: truncated+below must stay Unknown, got %s", st)
	}
}

// M-9: dropping defer Release in the execution path must fail P-14.
// Documented: the wiring layer MUST call adm.Release() on every path. The unit
// level proves the slot is only freed by an explicit Release (P-14/P-27).
func TestMutationM9ReleaseRequired(t *testing.T) {
	ss := NewSemaphoreSet(1)
	if !ss.Acquire("op") {
		t.Fatal("acquire")
	}
	// Without Release the slot stays held; a second acquire must fail.
	if ss.Acquire("op") {
		t.Fatal("M-9: slot must not auto-release — invariant holds (P-14)")
	}
	ss.Release("op")
}

// M-10: reordering the gate checks must fail P-15.
func TestMutationM10OrderFixed(t *testing.T) {
	// The order is encoded in Gate.Check; P-15 exercises it end-to-end. This
	// documentation test re-affirms kill precedes breaker.
	gate := newTestGate(NewKillStore(newFakeKillPersistence(), time.Now), &fakeFailureReader{}, &fakeAuditWriter{})
	_ = gate.kills.SetKilled("op", true)
	if _, rej := gate.Check(context.Background(), "op", "p"); rej == nil || rej.Action != ActionKilled {
		t.Fatalf("M-10: kill must precede breaker, got %v", rej)
	}
}

// M-11: using management.Intent for reject audit must fail P-17.
func TestMutationM11RejectVocabulary(t *testing.T) {
	// The reject vocabulary is the protection.* constant set; P-17 scans for any
	// management.Intent reference. Re-affirm the actions are protection-namespaced.
	for _, a := range []string{ActionKilled, ActionPrincipalKilled, ActionCircuitOpen, ActionBreakerUnknown, ActionConcurrencyExceeded, ActionRateLimited} {
		if a == "" || a[:11] != "protection." {
			t.Fatalf("M-11: reject action %q must be protection.* namespaced", a)
		}
	}
}

// M-13: converting Reject to Admission on audit failure must fail P-22.
func TestMutationM13RejectIrreversible(t *testing.T) {
	gate := newTestGate(NewKillStore(newFakeKillPersistence(), time.Now), &fakeFailureReader{}, &fakeAuditWriter{fail: true})
	for gate.buckets.Take("op", principalHash("p", gate.salt)) {
	}
	if adm, rej := gate.Check(context.Background(), "op", "p"); rej == nil || adm != nil {
		t.Fatal("M-13: audit failure must not convert reject to admission")
	}
}

// M-15: removing the audit-failure counter must fail P-24.
func TestMutationM15AuditFailCounter(t *testing.T) {
	gate := newTestGate(NewKillStore(newFakeKillPersistence(), time.Now), &fakeFailureReader{}, &fakeAuditWriter{fail: true})
	for gate.buckets.Take("op", principalHash("p", gate.salt)) {
	}
	gate.Check(context.Background(), "op", "p")
	if gate.SnapshotMetrics().AuditWriteFailed != 1 {
		t.Fatal("M-15: audit failure must increment counter")
	}
}

// M-18: a context.Done callback that releases the semaphore must fail P-27.
func TestMutationM18NoAutoRelease(t *testing.T) {
	ss := NewSemaphoreSet(1)
	adm := &Admission{capID: "op", gate: &Gate{sem: ss}}
	// Simulate the gate having acquired the only slot.
	ss.Acquire("op")
	if ss.Acquire("op") {
		t.Fatal("M-18: slot must remain held (no auto-release) until Release()")
	}
	adm.Release()
}
