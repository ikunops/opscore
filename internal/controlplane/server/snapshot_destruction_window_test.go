package server

// ADR-081 acceptance test: the destruction-window serialization (ADR-080's
// registered debt, closed). Two goroutines drive family compactions
// CONCURRENTLY through the production paths — verification attests and
// acceptance appends, each with a capacity that forces a compaction (hence a
// destruction window) on essentially every call. Before ADR-081, two windows
// that interlocked tripped the destruction log's chain-tail rule: the loser's
// completion was REFUSED and its destruction stayed `intended` forever
// (destruction_unconfirmed on the read face, the family's P49 accounting
// degraded to out_of_window). With the window mutex, every window completes:
// the assertions below pin zero intended-only groups, an empty unconfirmed
// list, and a conflict-free fully-verified destruction-anchor stream whose
// entry count equals the number of completed destructions.

import (
	"sync"
	"testing"
)

func TestADR081ConcurrentFamilyCompactionsCompleteEveryWindow(t *testing.T) {
	f := newP49Fixture(t, func(cfg *HistoryExportConfig) {
		cfg.DestructionLog = true
		cfg.DestructionCapacity = 4096
		cfg.AnchorCapacity = 4096
		cfg.VerifyCapacity = 1     // every attestation compacts ⇒ one destruction window per attest
		cfg.AcceptanceCapacity = 2 // from the 3rd append on, every append compacts ⇒ one window per append
	})
	f.driveAllFamilies()

	before, err := loadDestructionState(f.sched.destructionConfig())
	if err != nil {
		t.Fatal(err)
	}

	const rounds = 12
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // verification attests: one destruction window per attest
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if _, err := f.sched.AttestVerification(100 + i); err != nil {
				t.Errorf("attest %d: %v", i, err)
				return
			}
		}
	}()
	go func() { // acceptance appends: one destruction window per rotating append
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			f.append(p46At(i+10), int64(100+i))
		}
	}()
	wg.Wait()

	// Invariant 1 — EVERY destruction window completed: zero `intended`-only
	// groups. This is the assertion ADR-080's debt turns on: before the window
	// mutex, an interlocked window was refused ("no longer the chain tail") and
	// stayed `intended` forever.
	after, err := loadDestructionState(f.sched.destructionConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !after.verifiable {
		t.Fatalf("the destruction log must stay verifiable: %v", after.errs)
	}
	completed := 0
	for _, g := range after.groups {
		if !g.usable {
			t.Fatalf("destruction_seq %d became unusable (%s): %s", g.seq, g.verdict, g.detail)
		}
		switch g.state {
		case destructionStateCompleted:
			completed++
		case destructionStateIntended:
			t.Fatalf("ADR-081 VIOLATION: destruction_seq %d stayed `intended` — a window was refused under concurrency", g.seq)
		default:
			t.Fatalf("destruction_seq %d is in state %q", g.seq, g.state)
		}
	}

	// The acceptance family's window is COLLECT-ONLY (P47 B1): its completed
	// destruction waits in the scheduler's queue for the tick-tail drain. Run the
	// drain — the production shape — before asserting the anchor stream.
	f.sched.drainDestructionAnchorQueue()

	// Invariant 2 — the read face reports NO unconfirmed destruction: every
	// window is accounted, loudly and completely.
	view, err := f.sched.DestructionView()
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Unconfirmed) != 0 {
		t.Fatalf("ADR-081 VIOLATION: %v surfaced as destruction_unconfirmed", view.Unconfirmed)
	}

	// Invariant 3 — every completed window dispatched its anchor: the
	// destruction-anchor stream holds exactly one verified entry per completed
	// destruction, and it is conflict-free (two concurrent dispatches allocating
	// the same anchor_seq is the shape A7-⑦ predicted).
	anchors, err := loadAnchorStatePath(destructionAnchorPath(f.dir), f.dir, f.sched.trust)
	if err != nil {
		t.Fatal(err)
	}
	if len(anchors.conflicts) != 0 {
		t.Fatalf("the destruction-anchor stream must stay conflict-free: %v", anchors.conflicts)
	}
	if len(anchors.unusable) != 0 {
		t.Fatalf("the destruction-anchor stream holds %d unusable entry(ies): %+v", len(anchors.unusable), anchors.unusable)
	}
	if len(anchors.latest) != completed {
		t.Fatalf("every completed destruction must be anchored: %d completed, %d anchored", completed, len(anchors.latest))
	}

	// Sanity: the concurrent campaign actually opened windows (the fixture's own
	// group plus at least 2*rounds test-driven ones) — the test must not pass
	// vacuously.
	if minWant := len(before.groups) + 2*rounds - 2; completed < minWant {
		t.Fatalf("the campaign opened too few windows: %d completed, want >= %d (before=%d, rounds=%d)", completed, minWant, len(before.groups), rounds)
	}
}
