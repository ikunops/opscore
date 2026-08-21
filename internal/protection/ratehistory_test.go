package protection

import (
	"testing"
	"time"
)

// TestRateHistory_WindowDeltaInsufficient proves ok=false (conservative
// no-fire) when there is not enough history to be confident: zero samples,
// and a single sample both return ok=false.
func TestRateHistory_WindowDeltaInsufficient(t *testing.T) {
	h := NewRateHistory(time.Minute, 120)
	now := time.Now()

	if _, ok := h.WindowDelta(time.Minute, now); ok {
		t.Fatal("zero samples: ok must be false (conservative no-fire)")
	}

	h.Record(now, Metrics{Admitted: 5})
	if _, ok := h.WindowDelta(time.Minute, now); ok {
		t.Fatal("single sample: ok must be false (need a window baseline)")
	}
}

// TestRateHistory_WindowDeltaTooShort proves ok=false when no sample is older
// than the window cutoff — the buffer does not yet span a full window, so the
// delta would be meaningless (R24-3: never fire on missing evidence).
func TestRateHistory_WindowDeltaTooShort(t *testing.T) {
	h := NewRateHistory(time.Minute, 120)
	now := time.Now()
	// Both samples sit inside the trailing window (30s apart < 60s window).
	h.Record(now, Metrics{Admitted: 0})
	h.Record(now.Add(30*time.Second), Metrics{Admitted: 5})

	if _, ok := h.WindowDelta(time.Minute, now.Add(30*time.Second)); ok {
		t.Fatal("no sample older than the window cutoff: ok must be false")
	}
}

// TestRateHistory_WindowDeltaComputes proves the per-counter delta is derived
// from the newest sample at-or-before the window cutoff to the newest sample.
func TestRateHistory_WindowDeltaComputes(t *testing.T) {
	h := NewRateHistory(time.Minute, 120)
	t0 := time.Now()
	h.Record(t0, Metrics{Admitted: 10})
	h.Record(t0.Add(90*time.Second), Metrics{Admitted: 25})

	delta, ok := h.WindowDelta(time.Minute, t0.Add(90*time.Second))
	if !ok {
		t.Fatal("expected ok=true with samples spanning > window")
	}
	if delta.Admitted != 15 {
		t.Fatalf("delta.Admitted want 15 got %d", delta.Admitted)
	}
}
