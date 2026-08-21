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
