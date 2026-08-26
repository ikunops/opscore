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

// TransitionHistoryCapacity is the bounded in-memory OBSERVATION ring capacity
// for alert transitions. It is NOT a persistence layer (R24-5 Projection Only):
// its contents are lost on restart and must never be read as a Source of Truth.
const TransitionHistoryCapacity = 256

// AlertTransition is a PURE edge event recorded by AlertTracker.Observe when the
// firing state actually changes (P29-M2):
//   From=false, To=true  -> FIRING  (rising edge)
//   From=true,  To=false -> CLEAR   (falling edge)
// Observations with no state change (true->true, false->false) are NEVER
// recorded. The first Observe(firing=true) forms a false->true FIRING edge
// (the prior tracker state is false); the first Observe(firing=false) records
// NO transition (there is no prior true state to clear).
type AlertTransition struct {
	At          time.Time // observation time: when AlertTracker received the edge (P29-S1)
	From        bool
	To          bool
	UnknownRate int64
	Threshold   int64
}

// TransitionHistoryStats is the honest completeness block for the transition
// ring (R24-7 provenance loss honesty, P29-M1). Truncated is true ONLY when
// overflow actually dropped records (dropped>0) — never merely because the ring
// reached capacity. Hence "256 retained, 0 dropped, truncated=false" is
// honestly distinguishable from "256 retained, 17 dropped, truncated=true".
type TransitionHistoryStats struct {
	Capacity  int
	Retained  int
	Dropped   int64
	Truncated bool
}

// AlertTracker holds the transient alert state (the firing-entry time). It is
// fed by Observe from a background ticker and read by the management handler.
// It is NOT a Source of Truth (R24-5): it is derived from RateHistory.
type AlertTracker struct {
	mu             sync.Mutex
	firing         bool
	since          time.Time
	lastUnknown    int64
	lastThreshold  int64
	lastWindow     time.Duration

	// transition ring (P29): bounded observation buffer of edge events, written
	// ONLY inside Observe. It is an observation by-product, not a replay engine.
	transitions    []AlertTransition
	historyDropped int64
}

// NewAlertTracker builds an empty (not-firing) tracker.
func NewAlertTracker() *AlertTracker { return &AlertTracker{} }

// Observe folds a freshly-computed condition into the tracker state and returns
// the resulting alert state. On the rising edge (not-firing -> firing) it
// records Since and a FIRING transition; on the falling edge it clears Since
// and records a CLEAR transition. Observations with NO state change record
// neither (P29-M2). It never blocks the caller (an unlocked mutex over a few
// fields).
func (t *AlertTracker) Observe(cond AlertCondition, now time.Time) AlertState {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cond.Firing && !t.firing {
		t.firing = true
		t.since = now
		t.pushTransitionLocked(now, false, true, cond) // FIRING edge
	} else if !cond.Firing && t.firing {
		t.firing = false
		t.since = time.Time{}
		t.pushTransitionLocked(now, true, false, cond) // CLEAR edge
	}
	// Sustained firing OR first firing=false: NO transition recorded (P29-M2).
	t.lastUnknown = cond.UnknownRate
	t.lastThreshold = cond.Threshold
	t.lastWindow = cond.Window
	return t.stateLocked()
}

// pushTransitionLocked appends an edge event to the bounded ring, dropping the
// oldest record (and counting it) when at capacity (R24-7 loss honesty, P29-M1).
func (t *AlertTracker) pushTransitionLocked(now time.Time, from, to bool, cond AlertCondition) {
	tr := AlertTransition{
		At:          now,
		From:        from,
		To:          to,
		UnknownRate: cond.UnknownRate,
		Threshold:   cond.Threshold,
	}
	if len(t.transitions) >= TransitionHistoryCapacity {
		t.transitions = t.transitions[1:] // drop-oldest
		t.historyDropped++
	}
	t.transitions = append(t.transitions, tr)
}

// State returns the current alert state without changing it.
func (t *AlertTracker) State() AlertState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.stateLocked()
}

// Transitions returns a COPY of the transition ring in NEWEST-FIRST order
// (P29-S2 SHOULD: consistent with the provenance/audit read surface; "Recent"
// UI consumers take the first N). Callers cannot mutate internal state (P29-I6).
func (t *AlertTracker) Transitions() []AlertTransition {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]AlertTransition, len(t.transitions))
	for i, tr := range t.transitions {
		out[len(t.transitions)-1-i] = tr // reverse -> newest first
	}
	return out
}

// HistoryStats returns the honest completeness block (R24-7 / P29-M1).
func (t *AlertTracker) HistoryStats() TransitionHistoryStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	return TransitionHistoryStats{
		Capacity:  TransitionHistoryCapacity,
		Retained:  len(t.transitions),
		Dropped:   t.historyDropped,
		Truncated: t.historyDropped > 0, // P29-M1: ONLY overflow drops truncate
	}
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
