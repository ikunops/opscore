package protection

import (
	"sync"
	"time"
)

// AlertPolicy is the declarative alert configuration. It is the ONLY alert
// tuning surface; the server COMPUTES + EXPOSES the alert state from it but
// never transports or triggers anything (R24-3 Alerting Declarative).
type AlertPolicy struct {
	// Window is the sliding window over which the unknown-rate is measured.
	Window time.Duration
	// ThresholdUnknownRate is the unknown-decision rate per window that fires
	// the alert. "Unknown" decisions are breaker-unknown + quota-evidence-
	// unavailable (both are fail-closed rejections caused by MISSING evidence,
	// the operational signal Phase 24.2 surfaces). A value <= 0 disables firing.
	ThresholdUnknownRate int64
}

// DefaultAlertPolicy is the Phase 24.2-accepted default: fire when the unknown
// decision rate reaches 50 per minute.
func DefaultAlertPolicy() AlertPolicy {
	return AlertPolicy{
		Window:               time.Minute,
		ThresholdUnknownRate: 50,
	}
}

// AlertCondition is a PURE function result (R24-3): it carries the firing
// decision and the inputs that produced it, but holds NO state — notably it
// does NOT capture "since when". State (the firing-entry time) lives only in
// AlertTracker. ComputeAlertCondition is deterministic and side-effect free.
type AlertCondition struct {
	Firing      bool
	UnknownRate int64
	Threshold   int64
	Window      time.Duration
}

// ComputeAlertCondition evaluates the alert condition from a rate delta. Pure:
// given the same delta + policy it always returns the same condition, and it
// never reads time or mutates anything. The `now` used by the caller is only
// for windowing inside RateHistory, not inside this function.
func ComputeAlertCondition(delta Metrics, policy AlertPolicy) AlertCondition {
	unknown := delta.BreakerUnknown + delta.QuotaEvidenceUnavailable
	return AlertCondition{
		Firing:      policy.ThresholdUnknownRate > 0 && unknown >= policy.ThresholdUnknownRate,
		UnknownRate: unknown,
		Threshold:   policy.ThresholdUnknownRate,
		Window:      policy.Window,
	}
}

// AlertState is the observable alert state (R24-3: the server exposes this and
// nothing more). Since is the only state held — the time the alert entered the
// firing state. When not firing, Since is the zero time.
type AlertState struct {
	Firing       bool
	Since        time.Time
	UnknownRate  int64
	Threshold    int64
	Window       time.Duration
}

// AlertTracker holds the transient alert state (the firing-entry time). It is
// fed by Observe from a background ticker and read by the management handler.
// It is NOT a Source of Truth (R24-5): it is derived from RateHistory.
type AlertTracker struct {
	mu            sync.Mutex
	firing        bool
	since         time.Time
	lastUnknown   int64
	lastThreshold int64
	lastWindow    time.Duration
}

// NewAlertTracker builds an empty (not-firing) tracker.
func NewAlertTracker() *AlertTracker { return &AlertTracker{} }

// Observe folds a freshly-computed condition into the tracker state and returns
// the resulting alert state. On the rising edge (not-firing -> firing) it
// records Since; on the falling edge it clears Since. It never blocks the
// caller (an unlocked mutex over a few fields).
func (t *AlertTracker) Observe(cond AlertCondition, now time.Time) AlertState {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cond.Firing && !t.firing {
		t.firing = true
		t.since = now
	} else if !cond.Firing && t.firing {
		t.firing = false
		t.since = time.Time{}
	}
	t.lastUnknown = cond.UnknownRate
	t.lastThreshold = cond.Threshold
	t.lastWindow = cond.Window
	return t.stateLocked()
}

// State returns the current alert state without changing it.
func (t *AlertTracker) State() AlertState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stateLocked()
}

func (t *AlertTracker) stateLocked() AlertState {
	return AlertState{
		Firing:      t.firing,
		Since:       t.since,
		UnknownRate: t.lastUnknown,
		Threshold:   t.lastThreshold,
		Window:      t.lastWindow,
	}
}
