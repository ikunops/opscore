package protection

import (
	"testing"
	"time"
)

// TestComputeAlertCondition_Pure proves the alert condition is a pure function
// of the delta + policy: unknown rate = breaker_unknown + quota_evidence_
// unavailable; firing iff it reaches the threshold; threshold<=0 disables.
func TestComputeAlertCondition_Pure(t *testing.T) {
	policy := DefaultAlertPolicy() // threshold 50 / minute

	firing := ComputeAlertCondition(Metrics{BreakerUnknown: 60}, policy)
	if !firing.Firing {
		t.Fatal("unknown rate 60 >= 50 should fire")
	}
	if firing.UnknownRate != 60 {
		t.Fatalf("UnknownRate want 60 got %d", firing.UnknownRate)
	}

	notFiring := ComputeAlertCondition(Metrics{BreakerUnknown: 40}, policy)
	if notFiring.Firing {
		t.Fatal("unknown rate 40 < 50 should not fire")
	}

	// Disabled policy (threshold 0) never fires.
	disabled := AlertPolicy{Window: time.Minute, ThresholdUnknownRate: 0}
	if ComputeAlertCondition(Metrics{BreakerUnknown: 999}, disabled).Firing {
		t.Fatal("threshold 0 must disable firing")
	}

	// Both unknown sources combine toward the threshold.
	combined := ComputeAlertCondition(Metrics{BreakerUnknown: 30, QuotaEvidenceUnavailable: 25}, policy)
	if !combined.Firing || combined.UnknownRate != 55 {
		t.Fatalf("combined unknown 55 should fire, got firing=%v rate=%d", combined.Firing, combined.UnknownRate)
	}
}

// TestAlertTracker_SinceSemantics proves the tracker holds only the firing-entry
// time: it is set on the rising edge, held across sustained firing, and cleared
// on the falling edge (R24-3: state lives in the tracker, not in the pure
// condition).
func TestAlertTracker_SinceSemantics(t *testing.T) {
	tr := NewAlertTracker()
	now := time.Now()

	st := tr.State()
	if st.Firing || !st.Since.IsZero() {
		t.Fatalf("initial state must be not-firing with zero Since, got %+v", st)
	}

	rising := tr.Observe(AlertCondition{Firing: true, UnknownRate: 99, Threshold: 50, Window: time.Minute}, now)
	if !rising.Firing || rising.Since.IsZero() {
		t.Fatalf("rising edge must record Since, got %+v", rising)
	}
	since := rising.Since

	// Sustained firing must NOT advance Since.
	sustain := tr.Observe(AlertCondition{Firing: true, UnknownRate: 80, Threshold: 50, Window: time.Minute}, now.Add(time.Second))
	if !sustain.Since.Equal(since) {
		t.Fatalf("Since must not change on sustained firing: was %v now %v", since, sustain.Since)
	}

	// Falling edge clears Since.
	falling := tr.Observe(AlertCondition{Firing: false, UnknownRate: 0, Threshold: 50, Window: time.Minute}, now.Add(2*time.Second))
	if falling.Firing || !falling.Since.IsZero() {
		t.Fatalf("falling edge must clear Since, got %+v", falling)
	}
}

// TestAlertTracker_Transitions_EdgeOnly proves P29-M2: only genuine edges are
// recorded. rising (false->true) -> FIRING, falling (true->false) -> CLEAR; no
// transition on sustained firing or on the first false. Order is as observed.
func TestAlertTracker_Transitions_EdgeOnly(t *testing.T) {
	tr := NewAlertTracker()
	now := time.Now()

	// First observe(firing=false) must NOT record a CLEAR (no prior true state).
	tr.Observe(AlertCondition{Firing: false, UnknownRate: 0, Threshold: 50, Window: time.Minute}, now)
	if got := tr.Transitions(); len(got) != 0 {
		t.Fatalf("T1: first false must produce no transition, got %d", len(got))
	}

	// Rising edge -> FIRING.
	tr.Observe(AlertCondition{Firing: true, UnknownRate: 70, Threshold: 50, Window: time.Minute}, now.Add(time.Second))
	// Sustained firing -> NO extra transition.
	tr.Observe(AlertCondition{Firing: true, UnknownRate: 80, Threshold: 50, Window: time.Minute}, now.Add(2*time.Second))
	// Falling edge -> CLEAR.
	tr.Observe(AlertCondition{Firing: false, UnknownRate: 0, Threshold: 50, Window: time.Minute}, now.Add(3*time.Second))
	// Second false (already cleared) -> NO transition.
	tr.Observe(AlertCondition{Firing: false, UnknownRate: 0, Threshold: 50, Window: time.Minute}, now.Add(4*time.Second))

	got := tr.Transitions()
	if len(got) != 2 {
		t.Fatalf("T1: want exactly 2 transitions (FIRING, CLEAR), got %d: %+v", len(got), got)
	}
	// Newest-first (P29-S2): [0] is the most recent edge (CLEAR, the false at
	// now+3s, rate 0); [1] is the earlier edge (FIRING, the true at now+1s, rate 70).
	if !got[0].From || got[0].To {
		t.Fatalf("T1: [0] must be CLEAR edge (true->false, newest), got from=%v to=%v", got[0].From, got[0].To)
	}
	if got[1].From || !got[1].To {
		t.Fatalf("T1: [1] must be FIRING edge (false->true), got from=%v to=%v", got[1].From, got[1].To)
	}
	if got[0].UnknownRate != 0 || got[1].UnknownRate != 70 {
		t.Fatalf("T1: unexpected rates: %+v", got)
	}
}

// TestAlertTracker_Transitions_NewestFirst proves P29-S2: Transitions() returns
// NEWEST-FIRST, so "recent" UI consumers take the first N.
func TestAlertTracker_Transitions_NewestFirst(t *testing.T) {
	tr := NewAlertTracker()
	base := time.Now()
	tr.Observe(AlertCondition{Firing: true, UnknownRate: 1, Threshold: 50, Window: time.Minute}, base)
	tr.Observe(AlertCondition{Firing: false, UnknownRate: 0, Threshold: 50, Window: time.Minute}, base.Add(time.Second))
	tr.Observe(AlertCondition{Firing: true, UnknownRate: 2, Threshold: 50, Window: time.Minute}, base.Add(2*time.Second))
	got := tr.Transitions()
	if len(got) != 3 {
		t.Fatalf("want 3 transitions, got %d", len(got))
	}
	// Newest first: last observed (rate 2) must be index 0.
	if got[0].UnknownRate != 2 {
		t.Fatalf("P29-S2: newest-first expected rate 2 at [0], got %d", got[0].UnknownRate)
	}
	if got[2].UnknownRate != 1 {
		t.Fatalf("P29-S2: oldest (rate 1) expected at tail, got %d", got[2].UnknownRate)
	}
}

// TestAlertTracker_HistoryStats_TruncatedOnOverflow proves P29-M1: Truncated is
// true ONLY when the ring actually overflowed (dropped>0), never merely because
// it reached capacity. Also confirms Transitions() length is capped at capacity
// and HistoryStats().Retained == capacity in that case.
func TestAlertTracker_HistoryStats_TruncatedOnOverflow(t *testing.T) {
	tr := NewAlertTracker()
	// Fill exactly to capacity with alternating edges (each edge records one).
	for i := 0; i < TransitionHistoryCapacity; i++ {
		firing := i%2 == 0
		tr.Observe(AlertCondition{Firing: firing, UnknownRate: int64(i), Threshold: 50, Window: time.Minute}, time.Now().Add(time.Duration(i)*time.Second))
	}
	hs := tr.HistoryStats()
	if hs.Retained != TransitionHistoryCapacity {
		t.Fatalf("retained want %d got %d", TransitionHistoryCapacity, hs.Retained)
	}
	// Reached capacity but no overflow yet -> NOT truncated.
	if hs.Dropped != 0 || hs.Truncated {
		t.Fatalf("P29-M1: ring full but no overflow must NOT truncate (dropped=%d truncated=%v)", hs.Dropped, hs.Truncated)
	}
	if len(tr.Transitions()) != TransitionHistoryCapacity {
		t.Fatalf("transitions length want %d got %d", TransitionHistoryCapacity, len(tr.Transitions()))
	}

	// One more edge overflows the ring: dropped++ and truncated becomes true.
	tr.Observe(AlertCondition{Firing: true, UnknownRate: 999, Threshold: 50, Window: time.Minute}, time.Now().Add(time.Hour))
	hs2 := tr.HistoryStats()
	if hs2.Dropped != 1 {
		t.Fatalf("P29-M1: overflow must count 1 dropped, got %d", hs2.Dropped)
	}
	if !hs2.Truncated {
		t.Fatalf("P29-M1: dropped>0 must set truncated=true")
	}
	if hs2.Retained != TransitionHistoryCapacity {
		t.Fatalf("P29-M1: retained stays at capacity after overflow, got %d", hs2.Retained)
	}
	if len(tr.Transitions()) != TransitionHistoryCapacity {
		t.Fatalf("transitions length must remain capped at capacity, got %d", len(tr.Transitions()))
	}
}
