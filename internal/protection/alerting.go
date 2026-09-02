package protection

import (
	"context"
	"errors"
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
	Firing      bool
	Since       time.Time
	UnknownRate int64
	Threshold   int64
	Window      time.Duration
}

// TransitionHistoryCapacity is the bounded in-memory OBSERVATION ring capacity
// for alert transitions. It is NOT a persistence layer (R24-5 Projection Only):
// its contents are an observation by-product. Phase 30 ADDS durable,
// cross-restart retention via AlertTransitionStore (see below) — the ring is
// the live/recent view, the store is the durable view.
const TransitionHistoryCapacity = 256

// FileTransitionCapacity is the durable, cross-restart cap for persisted alert
// transitions (Phase 30). On overflow the oldest persisted record is evicted
// and FileDropped is incremented (honest loss accounting, R24-7).
const FileTransitionCapacity = 10000

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

// TransitionLoadResult is what a durable store returns from Load. It surfaces
// the recovered transitions plus honest completeness/error signals so the
// tracker (and the read API) can NEVER present a false-clean state.
//   - Transitions: recovered edge events (may be empty, which is honest "no history").
//   - FileDropped: persisted eviction count from the durable layer.
//   - RetentionMetaInconsistent: true when the durable metadata record could not
//     be parsed (so FileDropped is itself untrustworthy) — surfaced honestly,
//     never coerced to 0 / false-clean (P30-I10).
//   - Corrupt: true when a NON-TRAILING transition line could not be parsed
//     (P30-I12). Only a trailing partial line is legitimate crash-recovery
//     (ADR-053); corruption in the middle means durable history genuinely lost
//     records, so the store MUST signal it instead of silently `continue`-ing
//     and presenting the remainder as normal history (new false-clean risk).
//   - LoadErr: non-nil on any I/O/permission failure. A load failure
//     is NOT "no history"; it is "unknown prior state" (P30-I11).
//   - ExportedAt: the store-side wall-clock timestamp at which THIS snapshot
//     was materialized (Phase 34 provenance). It is the STORE's time, never
//     the scheduler's clock (P34-CLOCK-1): the scheduler must never derive
//     ExportedAt from its own clock, so a snapshot's exported_at stays honest
//     about when the durable history was read, not when the ticker fired.
type TransitionLoadResult struct {
	Transitions                []AlertTransition
	FileDropped                int64
	RetentionMetaInconsistent bool
	Corrupt                    bool
	LoadErr                    error
	ExportedAt                 time.Time
}

// AlertTransitionStore is the durable, cross-restart retention boundary for
// alert transitions (P30-I8). The interface lives in the protection core so the
// core depends on abstraction, not on a concrete file; the JSONL implementation
// lives in the controlplane/server package (storage boundary). A nil store
// means "in-memory only" (Phase 29 behavior, used by memory mode + tests).
type AlertTransitionStore interface {
	// Append records one transition durably and IN ORDER (P30-I9: synchronous,
	// no goroutine reordering). It must not block the caller indefinitely; a
	// best-effort failure is reported but durability is not guaranteed (P30-I6).
	Append(ctx context.Context, t AlertTransition) error
	// Load recovers persisted transitions + honest completeness metadata.
	Load(ctx context.Context) TransitionLoadResult
	// ReadRecent (Phase 31, P31-I6) is a BOUNDED durable READ projection: it
	// returns up to n persisted transitions, NEWEST-FIRST, under the caller's
	// read budget. It is NOT a replay and NEVER re-evaluates the alert
	// (P31-I2). It reports the same honesty signals as Load (P31-I5).
	ReadRecent(ctx context.Context, n int) TransitionReadResult
	// ReadAll (Phase 33) is the FULL durable READ projection for bulk export: it
	// returns EVERY currently-retained transition, NEWEST-FIRST, under the
	// caller's read budget — NOT clamped to DurableReadMaxLimit (1000). Its
	// entire reason for existing is the P32 gap where the only durable reads
	// were bounded to 1000/page or the 256-ring snapshot; an export must stream
	// the whole retained tail in one shot. Same honesty signals as Load.
	ReadAll(ctx context.Context) TransitionReadResult
	// ReadBefore (Phase 32) is the BOUNDED durable PAGING projection. It
	// returns up to n persisted transitions that precede `cursor`,
	// NEWEST-FIRST. An empty cursor starts from the newest record.
	//
	// `cursor` is an OPAQUE durable position token minted by the store
	// (returned as NextCursor). Clients must not parse, construct, or derive it
	// from a record's timestamp — timestamps are neither unique nor strictly
	// monotonic (P32-I9). Ordering is governed by P32-I11.
	ReadBefore(ctx context.Context, cursor string, n int) TransitionPageResult
	// Close releases durable resources.
	Close() error
}

// TransitionPageResult is the result of one bounded durable PAGE (Phase 32).
// It carries the page plus THIS read's honesty signals (P31-I11: never masked
// by the tracker's startup stats) and the paging metadata the server returns
// verbatim (next_cursor is server-minted; the client must never derive the
// next boundary from the last record's timestamp).
type TransitionPageResult struct {
	Transitions []AlertTransition
	FileDropped int64
	// RetentionMetaInconsistent and Corrupt are THIS read's findings (P31-I11).
	RetentionMetaInconsistent bool
	Corrupt                   bool
	// LoadErr carries I/O failures AND cursor failures (sentinels below).
	LoadErr error
	// HasMore is true when records older than this page remain in retention.
	HasMore bool
	// NextCursor is the opaque token for the following (older) page; "" when
	// HasMore is false.
	NextCursor string
}

// Cursor sentinels (Phase 32, P32-I10 Cursor Integrity). They live in the core
// so the store and the HTTP mapping cannot drift: a cursor problem is ALWAYS
// reported explicitly — never resolved by jumping to another position, falling
// back to the memory ring, or reinterpreting the token.
var (
	// ErrInvalidCursor: the token cannot be parsed at all (HTTP 400).
	ErrInvalidCursor = errors.New("invalid durable cursor")
	// ErrCursorExpired: well-formed token, but the record it identifies is no
	// longer in durable retention (HTTP 410). Per P32-I12 this is decided by
	// "is the record still retained", NOT by whether its byte offset moved
	// after an eviction rewrite.
	ErrCursorExpired = errors.New("durable cursor expired: record evicted")
	// ErrCursorAmbiguous: the token's identity matches more than one retained
	// record, so the position cannot be determined (HTTP 409). Never guess.
	ErrCursorAmbiguous = errors.New("durable cursor ambiguous")
)

// TransitionReadResult is the result of a bounded durable read projection
// (Phase 31). It is deliberately an ALIAS of TransitionLoadResult, not a new
// struct: the durable deep-read and the startup Load face the same file with
// the same corruption semantics, so they MUST share one honesty vocabulary
// (P31-I5). Two separate structs would eventually drift into "startup says
// clean / deep-read says corrupt", which is itself a false-clean source.
type TransitionReadResult = TransitionLoadResult

// DurableReadMaxLimit is the maximum number of persisted transitions a single
// durable read may return (P31-I3). It is a protocol-level cap shared by the
// handler's clamp and the store's defensive clamp, so the two can never
// disagree about what "bounded" means.
const DurableReadMaxLimit = 1000

// TransitionHistoryStats is the honest completeness block for the transition
// history (R24-7 provenance loss honesty, P29-M1). Truncated is true ONLY when
// overflow actually dropped records (dropped>0 OR file_dropped>0 OR retention
// metadata inconsistent) — never merely because the ring reached capacity.
// Hence "256 retained, 0 dropped, truncated=false" is honestly distinguishable
// from "256 retained, 17 dropped, truncated=true".
type TransitionHistoryStats struct {
	Capacity                    int
	Retained                    int
	Dropped                     int64
	Truncated                   bool
	FileDropped                 int64
	RetentionMetaInconsistent   bool
	Available                   bool
	LoadError                   bool
	// HistoryCorrupt is true when the durable history contains a non-trailing
	// unparseable line (P30-I12). It is STICKY: once set, the tracker never
	// presents itself as clean again — Available stays false for the process
	// lifetime, because durable records really were lost.
	HistoryCorrupt bool
}

// AlertTracker holds the transient alert state (the firing-entry time) plus a
// bounded recent-transition ring and (optionally) a durable store. It is fed by
// Observe from a background ticker and read by the management handler. It is
// NOT a Source of Truth (R24-5): it is derived from RateHistory.
type AlertTracker struct {
	mu sync.Mutex
	firing     bool
	since      time.Time
	lastUnknown   int64
	lastThreshold int64
	lastWindow    time.Duration

	// transition ring (P29): bounded observation buffer of edge events, written
	// ONLY inside Observe. It is an observation by-product, not a replay engine.
	transitions    []AlertTransition
	historyDropped int64

	// durable retention (Phase 30)
	store AlertTransitionStore

	// historyLoaded marks that a successful Load (or the in-memory baseline)
	// has completed. Until then, post-failed-load Observe calls establish a
	// baseline instead of emitting a synthetic edge (P30-I11).
	historyLoaded bool
	// loadError marks that the durable Load failed. The next Observe treats the
	// current condition as baseline (no edge, no synthetic FIRING).
	loadError bool
	// historyCorrupt marks non-trailing durable corruption (P30-I12). Sticky:
	// once true the tracker is permanently degraded (Available=false), because
	// recovered history genuinely lost records and must never look clean.
	historyCorrupt bool
	// retentionMetaInconsistent marks unparseable durable metadata (P30-I10).
	retentionMetaInconsistent bool
	// fileDropped is the durable-layer eviction count recovered at Load.
	fileDropped int64
}

// NewAlertTracker builds an empty (not-firing) in-memory-only tracker
// (Phase 29 behavior; store=nil). Used by tests and memory mode.
func NewAlertTracker() *AlertTracker { return NewAlertTrackerWithStore(nil) }

// NewAlertTrackerWithStore builds a tracker and, when store != nil, recovers
// durable history from it (Phase 30):
//   - Load error        -> honest "unknown prior state": loadError=true, next
//                           Observe establishes baseline, no synthetic edge (P30-I11).
//   - Clean empty load  -> firing=false, empty ring.
//   - Clean non-empty   -> firing = last transition's To (P30-I7 restart
//                           reconstruction); ring seeded with the MOST RECENT
//                           256 (NOT everything, which would inflate Dropped).
//   - Inconsistent meta -> retentionMetaInconsistent=true surfaced honestly
//                           (P30-I10); transitions still recovered best-effort.
func NewAlertTrackerWithStore(store AlertTransitionStore) *AlertTracker {
	t := &AlertTracker{store: store}
	if store == nil {
		t.historyLoaded = true // in-memory ring is always available
		return t
	}
	res := store.Load(context.Background())
	if res.LoadErr != nil || res.Corrupt {
		// P30-I11: load failure != "no history". P30-I12: a non-trailing corrupt
		// line is also NOT clean history (records really were lost). Both paths
		// degrade honestly — baseline on next Observe, never a synthetic edge.
		t.loadError = res.LoadErr != nil
		t.historyCorrupt = res.Corrupt
		t.historyLoaded = false
		return t
	}
	t.retentionMetaInconsistent = res.RetentionMetaInconsistent
	t.fileDropped = res.FileDropped
	if len(res.Transitions) > 0 {
		last := res.Transitions[len(res.Transitions)-1]
		t.firing = last.To // P30-I7: reconstruct current state from last edge
		start := 0
		if len(res.Transitions) > TransitionHistoryCapacity {
			start = len(res.Transitions) - TransitionHistoryCapacity
		}
		t.transitions = append([]AlertTransition(nil), res.Transitions[start:]...)
	}
	t.historyLoaded = true
	return t
}

// Observe folds a freshly-computed condition into the tracker state and returns
// the resulting alert state. On the rising edge (not-firing -> firing) it
// records Since and a FIRING transition; on the falling edge it clears Since
// and records a CLEAR transition. Observations with NO state change record
// neither (P29-M2). When a durable store is attached, each edge is also
// appended synchronously (P30-I9 ordered, P30-I6 best-effort). It never blocks
// the caller beyond a synchronous in-ring op + best-effort file append.
func (t *AlertTracker) Observe(cond AlertCondition, now time.Time) AlertState {
	t.mu.Lock()
	defer t.mu.Unlock()

	// P30-I11/P30-I12: after a failed durable Load (I/O error OR non-trailing
	// corruption), the first Observe establishes the baseline (current firing
	// state) WITHOUT emitting a synthetic edge. This avoids the
	// restart-while-FIRING false FIRING transition and never invents history.
	if t.store != nil && (t.loadError || t.historyCorrupt) && !t.historyLoaded {
		t.firing = cond.Firing
		if cond.Firing {
			t.since = now
		} else {
			t.since = time.Time{}
		}
		t.historyLoaded = true // baseline established; normal edges resume
		t.lastUnknown = cond.UnknownRate
		t.lastThreshold = cond.Threshold
		t.lastWindow = cond.Window
		return t.stateLocked()
	}

	if cond.Firing && !t.firing {
		t.firing = true
		t.since = now
		t.recordEdgeLocked(now, false, true, cond) // FIRING edge
	} else if !cond.Firing && t.firing {
		t.firing = false
		t.since = time.Time{}
		t.recordEdgeLocked(now, true, false, cond) // CLEAR edge
	}
	// Sustained firing OR first firing=false: NO transition recorded (P29-M2).
	t.lastUnknown = cond.UnknownRate
	t.lastThreshold = cond.Threshold
	t.lastWindow = cond.Window
	return t.stateLocked()
}

// recordEdgeLocked appends an edge event to the bounded ring (drop-oldest +
// count) and, when a durable store is attached and history is established,
// appends it synchronously (best-effort) so on-disk order == observation order
// (P30-I9). Caller MUST hold t.mu.
func (t *AlertTracker) recordEdgeLocked(now time.Time, from, to bool, cond AlertCondition) {
	t.pushTransitionLocked(now, from, to, cond) // ring update
	if t.store != nil && t.historyLoaded {
		// Append the just-pushed transition (now last in the slice).
		_ = t.store.Append(context.Background(), t.transitions[len(t.transitions)-1]) // P30-I6 best-effort
	}
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

// HistoryStats returns the honest completeness block (R24-7 / P29-M1 /
// Phase 30). Truncated is true if ANY layer actually dropped records OR the
// retention metadata is inconsistent — never merely because a buffer reached
// capacity.
func (t *AlertTracker) HistoryStats() TransitionHistoryStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	truncated := t.historyDropped > 0 || t.fileDropped > 0 || t.retentionMetaInconsistent
	return TransitionHistoryStats{
		Capacity:                  TransitionHistoryCapacity,
		Retained:                  len(t.transitions),
		Dropped:                   t.historyDropped,
		Truncated:                 truncated,
		FileDropped:               t.fileDropped,
		RetentionMetaInconsistent: t.retentionMetaInconsistent,
		Available: t.historyLoaded && !t.loadError && !t.historyCorrupt,
		LoadError: t.loadError,
		// P30-I12: corruption is sticky and keeps Available=false forever.
		HistoryCorrupt: t.historyCorrupt,
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
