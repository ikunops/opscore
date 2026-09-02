package protection

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// ---- Phase 30: durable, cross-restart retention --------------------------

// fakeStore is a test double for AlertTransitionStore. It returns a preset Load
// result and records Append calls so tests can assert persistence behavior
// without touching the filesystem.
type fakeStore struct {
	loadResult TransitionLoadResult
	appended   []AlertTransition
}

func (f *fakeStore) Append(ctx context.Context, t AlertTransition) error {
	f.appended = append(f.appended, t)
	return nil
}
func (f *fakeStore) Load(ctx context.Context) TransitionLoadResult { return f.loadResult }
func (f *fakeStore) Close() error                                 { return nil }

// ReadBefore (Phase 32) pages the same preset data, NEWEST-FIRST. The double
// keeps the same honesty vocabulary as Load and understands a simple opaque
// "idx:<n>" cursor, so cursor semantics can be exercised without a filesystem.
func (f *fakeStore) ReadBefore(ctx context.Context, cursor string, n int) TransitionPageResult {
	base := f.loadResult // copy: same honesty signals
	if base.LoadErr != nil || base.Corrupt {
		return TransitionPageResult{
			FileDropped:               base.FileDropped,
			RetentionMetaInconsistent: base.RetentionMetaInconsistent,
			Corrupt:                   base.Corrupt,
			LoadErr:                   base.LoadErr,
		}
	}
	end := len(base.Transitions)
	if cursor != "" {
		trimmed := strings.TrimPrefix(cursor, "idx:")
		idx, err := strconv.Atoi(trimmed)
		if err != nil || idx < 0 || idx > end {
			return TransitionPageResult{LoadErr: fmt.Errorf("%w: %q", ErrInvalidCursor, cursor)}
		}
		end = idx
	}
	start := 0
	if end > n {
		start = end - n
	}
	sel := base.Transitions[start:end]
	out := make([]AlertTransition, 0, len(sel))
	for i := len(sel) - 1; i >= 0; i-- {
		out = append(out, sel[i]) // NEWEST-FIRST
	}
	hasMore := start > 0
	next := ""
	if hasMore {
		next = "idx:" + strconv.Itoa(start)
	}
	return TransitionPageResult{
		Transitions:                out,
		FileDropped:                base.FileDropped,
		RetentionMetaInconsistent: base.RetentionMetaInconsistent,
		HasMore:                   hasMore,
		NextCursor:                next,
	}
}

// ReadRecent (Phase 31) serves the bounded durable read projection from the
// same preset data, NEWEST-FIRST (P31-I5). The double intentionally shares
// Load's honesty vocabulary — including its error/corruption signals — because
// that sharing is precisely the invariant under test.
func (f *fakeStore) ReadRecent(ctx context.Context, n int) TransitionReadResult {
	res := f.loadResult // copy: same honesty signals as Load (P31-I5)
	if res.LoadErr != nil || res.Corrupt {
		return res
	}
	txns := res.Transitions
	if n > 0 && n < len(txns) {
		txns = txns[len(txns)-n:]
	}
	out := make([]AlertTransition, 0, len(txns))
	for i := len(txns) - 1; i >= 0; i-- {
		out = append(out, txns[i])
	}
	res.Transitions = out
	return res
}

// ReadAll (Phase 33) returns the FULL retained set, NEWEST-FIRST — no per-call
// count clamp. It reuses the same honesty signals as ReadRecent/Load.
func (f *fakeStore) ReadAll(ctx context.Context) TransitionReadResult {
	res := f.loadResult
	if res.LoadErr != nil || res.Corrupt {
		return res
	}
	txns := res.Transitions
	out := make([]AlertTransition, 0, len(txns))
	for i := len(txns) - 1; i >= 0; i-- {
		out = append(out, txns[i])
	}
	res.Transitions = out
	return res
}

// TestAlertTracker_P30_RestartReconstruction (T1) proves P30-I7: after a clean
// non-empty Load, current firing reconstructs from the last transition's To, and
// the runtime ring is seeded with the MOST RECENT transitions (not replayed, so
// Dropped stays 0).
func TestAlertTracker_P30_RestartReconstruction(t *testing.T) {
	store := &fakeStore{loadResult: TransitionLoadResult{
		Transitions: []AlertTransition{
			{At: time.Now(), From: false, To: true, UnknownRate: 70, Threshold: 50}, // FIRING
			{At: time.Now(), From: true, To: false, UnknownRate: 0, Threshold: 50},  // CLEAR
		},
	}}
	tr := NewAlertTrackerWithStore(store)

	// P30-I7: firing reconstructs from last.To (false).
	if tr.State().Firing {
		t.Fatal("firing must reconstruct from last transition To=false")
	}
	// Ring seeded with both; newest-first: [CLEAR, FIRING].
	txns := tr.Transitions()
	if len(txns) != 2 {
		t.Fatalf("want 2 seeded transitions, got %d", len(txns))
	}
	if txns[0].From != true || txns[0].To != false {
		t.Fatalf("newest must be CLEAR (true->false), got %+v", txns[0])
	}
	if txns[1].From != false || txns[1].To != true {
		t.Fatalf("oldest must be FIRING (false->true), got %+v", txns[1])
	}
	hs := tr.HistoryStats()
	if !hs.Available {
		t.Fatal("Available must be true after clean load")
	}
	if hs.LoadError {
		t.Fatal("LoadError must be false after clean load")
	}
	if hs.Dropped != 0 {
		t.Fatalf("seeding must NOT inflate Dropped, got %d", hs.Dropped)
	}
	// A new rising edge persists exactly one transition.
	tr.Observe(AlertCondition{Firing: true, UnknownRate: 80, Threshold: 50, Window: time.Minute}, time.Now())
	if len(store.appended) != 1 {
		t.Fatalf("persist exactly 1 append on new edge, got %d", len(store.appended))
	}
}

// TestAlertTracker_P30_LoadFailureHonesty (T4) proves P30-I11: a Load error is
// NOT "no history". The next Observe establishes a baseline (no synthetic edge,
// no append), and HistoryStats exposes load_error=true / available=false. A
// subsequent genuine edge then records + persists normally.
func TestAlertTracker_P30_LoadFailureHonesty(t *testing.T) {
	store := &fakeStore{loadResult: TransitionLoadResult{LoadErr: errors.New("disk gone")}}
	tr := NewAlertTrackerWithStore(store)

	hs := tr.HistoryStats()
	if !hs.LoadError {
		t.Fatal("LoadError must be true on failed load")
	}
	if hs.Available {
		t.Fatal("Available must be false on failed load")
	}
	// First Observe(true) after a failed load -> baseline, NO synthetic edge.
	st := tr.Observe(AlertCondition{Firing: true, UnknownRate: 70, Threshold: 50, Window: time.Minute}, time.Now())
	if !st.Firing {
		t.Fatal("baseline must set firing=true")
	}
	if len(tr.Transitions()) != 0 {
		t.Fatalf("no synthetic transition after failed load, got %d", len(tr.Transitions()))
	}
	if len(store.appended) != 0 {
		t.Fatalf("no append on baseline, got %d", len(store.appended))
	}
	// A genuine falling edge now records + persists.
	tr.Observe(AlertCondition{Firing: false, UnknownRate: 0, Threshold: 50, Window: time.Minute}, time.Now())
	if len(tr.Transitions()) != 1 {
		t.Fatalf("want 1 transition after real edge, got %d", len(tr.Transitions()))
	}
	if len(store.appended) != 1 {
		t.Fatalf("want 1 append after real edge, got %d", len(store.appended))
	}
}

// TestAlertTracker_P30_MemoryNil (T7) proves memory mode (store=nil) keeps the
// Phase 29 behavior: available, no load error, no persistence, ring only.
func TestAlertTracker_P30_MemoryNil(t *testing.T) {
	tr := NewAlertTracker()
	hs := tr.HistoryStats()
	if !hs.Available {
		t.Fatal("memory mode must be available")
	}
	if hs.LoadError {
		t.Fatal("memory mode must have no load error")
	}
	now := time.Now()
	// First false -> no transition (classic P29-M2).
	tr.Observe(AlertCondition{Firing: false, UnknownRate: 0, Threshold: 50, Window: time.Minute}, now)
	if len(tr.Transitions()) != 0 {
		t.Fatalf("memory mode: first false must produce no transition, got %d", len(tr.Transitions()))
	}
	// Rising -> FIRING, retained in ring.
	tr.Observe(AlertCondition{Firing: true, UnknownRate: 70, Threshold: 50, Window: time.Minute}, now.Add(time.Second))
	if len(tr.Transitions()) != 1 {
		t.Fatalf("memory mode: want 1 transition, got %d", len(tr.Transitions()))
	}
}

